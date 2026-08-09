package wsp

import (
	"regexp"
	"strings"
)

// maxTaskIDLen bounds the task id so a workspace dir name stays comfortably
// inside filesystem limits even with a slug appended.
const maxTaskIDLen = 64

// taskIDRe is the decided task-id rule: start alphanumeric, then alphanumerics,
// dot, underscore or dash. It excludes anything that would make the id a path
// (a slash), a hidden file (a leading dot), or an option-looking argument (a
// leading dash).
var taskIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidTaskID reports whether id may name a workspace: it matches
// ^[A-Za-z0-9][A-Za-z0-9._-]*$ and is at most 64 bytes. Callers reject a
// violation as a usage error (exit 2).
func ValidTaskID(id string) bool {
	return len(id) <= maxTaskIDLen && taskIDRe.MatchString(id)
}

// DirName is the workspace directory name for a task: "<task_id>" when there
// is no usable description, otherwise "<task_id>_<slug>". Keeping the task id
// as the leading segment is what lets a human (and `workspace ls`) read the
// identity straight off the directory.
func DirName(taskID, desc string) string {
	s := slug(desc)
	if s == "" {
		return taskID
	}
	return taskID + "_" + s
}

// slug renders free text as a directory-safe fragment: lowercased, every run of
// characters outside [a-z0-9] collapsed to a single '-', and leading/trailing
// '-' trimmed. Text with nothing usable in it (symbols, spaces, non-ASCII)
// slugs to the empty string rather than to a row of dashes.
func slug(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true // collapsed: written only if a kept character follows
	}
	return b.String()
}
