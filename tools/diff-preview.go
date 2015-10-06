package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

/*
A tool to preview changes before checkin.
Uses WinMerge to do the diffing (http://winmerge.org/)
Anohter option that wouldn't require winmerge is to make it a web server,
implement web-based ui and launch the browser.
*/

var (
	gitPath      string
	winMergePath string
	tempDir      string
)

const (
	Modified = iota
	Added
	Deleted
	NotCheckedIn
)

type GitChange struct {
	Type int // Modified, Added etc.
	Path string
	Name string
}

func printStack() {
	buf := make([]byte, 1024*164)
	n := runtime.Stack(buf, false)
	fmt.Printf("%s", buf[:n])
}

func fatalf(format string, args ...interface{}) {
	fmt.Printf(format, args...)
	printStack()
	os.Exit(1)
}

func fatalif(cond bool, format string, args ...interface{}) {
	if cond {
		fmt.Printf(format, args...)
		printStack()
		os.Exit(1)
	}
}

func fataliferr(err error) {
	if err != nil {
		fatalf("%s\n", err.Error())
	}
}

func toTrimmedLines(d []byte) []string {
	lines := strings.Split(string(d), "\n")
	i := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		// remove empty lines
		if len(l) > 0 {
			lines[i] = l
			i++
		}
	}
	return lines[:i]
}

func detectExeMust(name string) string {
	path, err := exec.LookPath(name)
	if err == nil {
		fmt.Printf("'%s' is '%s'\n", name, path)
		return path
	}
	fmt.Printf("Couldn't find '%s'\n", name)
	fataliferr(err)
	// TODO: could also try known locations for WinMerge in $env["ProgramFiles(x86)"]/WinMerge/WinMergeU.exe
	return ""
}

func detectExesMust() {
	gitPath = detectExeMust("git")
	winMergePath = detectExeMust("WinMergeU")
}

func getWinTempDirMust() string {
	dir := os.Getenv("TEMP")
	if dir != "" {
		return dir
	}
	dir = os.Getenv("TMP")
	fatalif(dir == "", "env variable TEMP and TMP are not set\n")
	return dir
}

func createTempDirMust() {
	dir := getWinTempDirMust()
	// we want a stable name so that we can clean up old junk
	tempDir = filepath.Join(dir, "sum-diff-preview")
	err := os.MkdirAll(tempDir, 0755)
	fataliferr(err)
}

func runCmd(exePath string, args ...string) ([]byte, error) {
	cmd := exec.Command(exePath, args...)
	fmt.Printf("running: %s %v\n", filepath.Base(exePath), args)
	return cmd.Output()
}

func parseGitStatusLineMust(s string) *GitChange {
	c := &GitChange{}
	parts := strings.SplitN(s, " ", 2)
	fatalif(len(parts) != 2, "invalid line: '%s'\n", s)
	switch parts[0] {
	case "M":
		c.Type = Modified
	case "A":
		c.Type = Added
	case "D":
		c.Type = Deleted
	case "??":
		c.Type = NotCheckedIn
	default:
		fatalif(true, "invalid line: '%s'\n", s)
	}
	c.Path = strings.TrimSpace(parts[1])
	c.Name = filepath.Base(c.Path)
	return c
}

func parseGitStatusMust(out []byte, includeNotCheckedIn bool) []*GitChange {
	var res []*GitChange
	lines := toTrimmedLines(out)
	for _, l := range lines {
		c := parseGitStatusLineMust(l)
		if !includeNotCheckedIn && c.Type == NotCheckedIn {
			continue
		}
		res = append(res, c)
	}
	return res
}

func gitStatusMust() []*GitChange {
	out, err := runCmd(gitPath, "status", "--porcelain")
	fataliferr(err)
	return parseGitStatusMust(out, false)
}

func gitGetFileContentHeadMust(path string) []byte {
	loc := "HEAD:" + path
	out, err := runCmd(gitPath, "show", loc)
	fataliferr(err)
	return out
}

// delete directories older than 1 day in tempDir
func deleteOldDirs() {
	files, err := ioutil.ReadDir(tempDir)
	fataliferr(err)
	for _, fi := range files {
		if !fi.IsDir() {
			// we shouldn't create anything but dirs
			continue
		}
		age := time.Now().Sub(fi.ModTime())
		path := filepath.Join(tempDir, fi.Name())
		if age > time.Hour*24 {
			fmt.Printf("Deleting %s because older than 1 day\n", path)
			err = os.RemoveAll(path)
			fataliferr(err)
		} else {
			fmt.Printf("Not deleting %s because younger than 1 day (%s)\n", path, age)
		}
	}
}

func getBeforeAfterDirs(dir string) (string, string) {
	dirBefore := filepath.Join(dir, "before")
	dirAfter := filepath.Join(dir, "after")
	return dirBefore, dirAfter
}

// http://manual.winmerge.org/Command_line.html
func runWinMerge(dir string) {
	dirBefore, dirAfter := getBeforeAfterDirs(dir)
	/*
		/e : close with Esc
		/u : don't add paths to MRU
		/wl, wr : open left/right as read-only
		/r : recursive compare
	*/
	_, err := runCmd(winMergePath, "/u", "/wl", "/wr", dirBefore, dirAfter)
	fataliferr(err)
}

func catGitHeadToFileMust(dst, gitPath string) {
	fmt.Printf("catGitHeadToFileMust: %s => %s\n", gitPath, dst)
	d := gitGetFileContentHeadMust(gitPath)
	f, err := os.Create(dst)
	fataliferr(err)
	defer f.Close()
	_, err = f.Write(d)
	fataliferr(err)
}

func createEmptyFileMust(path string) {
	f, err := os.Create(path)
	fataliferr(err)
	f.Close()
}

func copyFileMust(dst, src string) {
	// ensure windows-style dir separator
	dst = strings.Replace(dst, "/", "\\", -1)
	src = strings.Replace(src, "/", "\\", -1)

	fdst, err := os.Create(dst)
	fataliferr(err)
	defer fdst.Close()
	fsrc, err := os.Open(src)
	fataliferr(err)
	defer fsrc.Close()
	_, err = io.Copy(fdst, fsrc)
	fataliferr(err)
}

func copyFileAddedMust(dirBefore, dirAfter string, change *GitChange) {
	// empty file in before, content in after
	path := filepath.Join(dirBefore, change.Name)
	createEmptyFileMust(path)
	path = filepath.Join(dirAfter, change.Name)
	copyFileMust(path, change.Path)
}

func copyFileDeletedMust(dirBefore, dirAfter string, change *GitChange) {
	// empty file in after
	path := filepath.Join(dirAfter, change.Name)
	createEmptyFileMust(path)
	// version from HEAD in before
	path = filepath.Join(dirBefore, change.Name)
	catGitHeadToFileMust(path, change.Path)
}

func copyFileModifiedMust(dirBefore, dirAfter string, change *GitChange) {
	// current version on disk in after
	path := filepath.Join(dirAfter, change.Name)
	copyFileMust(path, change.Path)
	// version from HEAD in before
	path = filepath.Join(dirBefore, change.Name)
	catGitHeadToFileMust(path, change.Path)
}

func copyFileChangeMust(dir string, change *GitChange) {
	dirBefore, dirAfter := getBeforeAfterDirs(dir)
	switch change.Type {
	case Added:
		copyFileAddedMust(dirBefore, dirAfter, change)
	case Modified:
		copyFileModifiedMust(dirBefore, dirAfter, change)
	case Deleted:
		copyFileModifiedMust(dirBefore, dirAfter, change)
	default:
		fatalif(true, "unknown change %+v\n", change)
	}
}

func copyFiles(dir string, changes []*GitChange) {
	dirBefore, dirAfter := getBeforeAfterDirs(dir)
	err := os.MkdirAll(dirBefore, 0755)
	fataliferr(err)
	err = os.MkdirAll(dirAfter, 0755)
	fataliferr(err)
	for _, change := range changes {
		copyFileChangeMust(dir, change)
	}
}

func hasGitDirMust(dir string) bool {
	files, err := ioutil.ReadDir(dir)
	fataliferr(err)
	for _, fi := range files {
		if strings.ToLower(fi.Name()) == ".git" {
			return fi.IsDir()
		}
	}
	return false
}

// git status returns names relative to root of
func cdToGitRoot() {
	var newDir string
	dir, err := os.Getwd()
	fataliferr(err)
	for {
		if hasGitDirMust(dir) {
			break
		}
		newDir = filepath.Dir(dir)
		fatalif(dir == newDir, "dir == newDir (%s == %s)", dir, newDir)
		dir = newDir
	}
	if newDir != "" {
		fmt.Printf("Changed current dir to: '%s'\n", newDir)
		os.Chdir(newDir)
	}
}

func main() {
	detectExesMust()
	createTempDirMust()
	fmt.Printf("temp dir: %s\n", tempDir)
	deleteOldDirs()

	cdToGitRoot()
	changes := gitStatusMust()
	if len(changes) == 0 {
		fmt.Printf("No changes to preview!")
		os.Exit(0)
	}
	fmt.Printf("%d change(s)\n", len(changes))

	// TODO: verify GitChange.Name is unique in changes
	subDir := time.Now().Format("2006-01-02_15_04_05")
	dir := filepath.Join(tempDir, subDir)
	err := os.MkdirAll(dir, 0755)
	fataliferr(err)
	copyFiles(dir, changes)
	runWinMerge(dir)
}
