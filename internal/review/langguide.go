package review

import (
	"path/filepath"
	"strings"
)

// langGuide is review guidance for one language: the mistakes a generic
// review misses in it, and the findings that are false positives there.
type langGuide struct {
	name string
	exts []string
	text string
}

// langGuides are appended to the system prompt for the languages a review
// covers, in this order whatever the order of the files, so the same set of
// languages always gives the same prompt.
var langGuides = []langGuide{
	{"Go", []string{".go"}, `- Errors: an error ignored, or shadowed (err := in an inner scope hiding the err that is returned), or wrapped with %v where callers use errors.Is or errors.As.
- defer: in a loop it holds every resource until the function returns; its arguments are evaluated when it is deferred; a deferred Close on a file that was written loses the write error.
- Goroutines: data races on maps, slices or struct fields; a goroutine blocked forever on a channel no one reads or closes; WaitGroup.Add called inside the goroutine; a context not passed on or never cancelled.
- nil: writing to a nil map panics; a typed nil pointer stored in an interface is not == nil.
- Slices: append to a sub-slice can overwrite the original's backing array.
- Loop variables: from Go 1.22 (the go directive in go.mod), a variable the loop declares (for i := ..., for _, v := range ...) is new in each iteration, so capturing it in a closure or goroutine is safe. It is shared when the module targets an earlier Go, and a variable declared before the loop and assigned in it (for _, v = range ...) is always shared.
- Do not report: unchecked errors from fmt.Fprint* to a bytes.Buffer or strings.Builder, or from hash.Hash.Write, which cannot fail; missing doc comments.`},
	{"Python", []string{".py", ".pyi"}, `- Mutable default arguments; closures in a loop that capture the loop variable (late binding).
- async: a coroutine called without await; blocking calls (time.sleep, requests, synchronous file or socket IO) inside async def; a task from create_task that is not kept and may be garbage collected.
- Exceptions: a bare except or except Exception that swallows errors (a bare except also catches KeyboardInterrupt and SystemExit).
- Comparisons: is used for value equality; == None.
- Resources: files, sockets and locks used without with.
- Injection: subprocess with shell=True and interpolated input; SQL built with f-strings, % or +; pickle or yaml.load on untrusted data.
- Do not report: missing type hints, or style a formatter handles (line length, quotes, import order).`},
	{"JavaScript/TypeScript", []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts"}, `- Promises: a missing await (the error goes unhandled and the order is wrong); an async callback passed to forEach, which does not wait for it; sequential awaits where order does not matter, or Promise.all where it does.
- Types: as casts and non-null assertions (!) that hide a possible undefined; any leaking into typed code; an optional chain whose undefined result is then used as a value.
- Truthiness: == coercion; if (count) treating 0 or "" as missing; || where ?? is meant.
- React: missing or wrong hook dependencies; state set from a stale closure; hooks called conditionally; array indexes as keys on lists that reorder.
- Security: innerHTML or dangerouslySetInnerHTML with untrusted data; shell or SQL strings built from input; prototype pollution from merging untrusted JSON into objects.
- Do not report: formatting, semicolons, or style a linter (ESLint, Prettier) enforces.`},
	{"Rust", []string{".rs"}, `- Panics: unwrap, expect or indexing on input the code does not control.
- Integers: overflow (it wraps in release builds) and as casts that truncate or change sign.
- unsafe: aliasing &mut, references that outlive their data, invariants a SAFETY comment claims but the code does not keep.
- Concurrency: a std Mutex guard held across .await; blocking calls inside async code; RefCell borrows that panic at run time.
- Errors: a Result ignored with let _ =; ? conversions that lose the context needed to act on the error.
- Do not report: borrow or lifetime errors the compiler would reject (the code compiles), or clippy style lints.`},
	{"Java", []string{".java"}, `- Equality: equals overridden without hashCode; == on String or boxed numbers; mutable objects used as map keys.
- Resources: streams, connections and readers not in try-with-resources; an empty catch; catching Exception or Throwable broadly.
- Concurrency: unsynchronized shared mutable state; double-checked locking without volatile; HashMap or SimpleDateFormat shared across threads.
- null: Optional.get without a check; unboxing a null Integer; returning null where a collection is expected.
- Security: SQL built from strings (use PreparedStatement); deserializing untrusted data; XML parsers with external entities enabled.`},
	{"C/C++", []string{".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh"}, `- Memory: buffer overflows (strcpy, sprintf, unchecked lengths, off-by-one), use after free, double free, leaks on error paths, pointers to locals returned.
- Integers: signed overflow (undefined behaviour); size_t underflow in a subtraction; narrowing conversions in length checks.
- C++: references and iterators used after the container changed; a base class without a virtual destructor; the rule of three/five broken; exceptions thrown from destructors.
- A format string from input; unchecked results of malloc, read or write.
- Concurrency: data races and inconsistent lock ordering.`},
	{"Shell", []string{".sh", ".bash"}, `- Quoting: unquoted $var or $(cmd) splits on spaces and expands globs.
- Failures: without set -o pipefail a failing command in a pipeline is ignored; cd without || exit; rm -rf "$dir/" when $dir can be empty.
- Parsing ls output; [ $x = y ] with an empty $x; eval or unquoted input inside a command.
- Temporary files at predictable /tmp paths (use mktemp).`},
	{"SQL", []string{".sql"}, `- Migrations: operations that lock a large table (an index built without CONCURRENTLY on Postgres, a column rewrite); data changes with no way back.
- Queries: UPDATE or DELETE without a WHERE; = NULL instead of IS NULL; joins that multiply rows; functions on an indexed column in a WHERE, which stop the index being used.
- Do not report: keyword case or formatting.`},
}

// languageGuidance is the guidance for the languages of files, as a system
// prompt section, or "" when none of them has any.
func languageGuidance(files []string) string {
	var b strings.Builder
	for _, g := range langGuides {
		if !anyHasExt(files, g.exts) {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("\n\nLanguage-specific guidance for the files in this review:\n")
		}
		b.WriteString("\n" + g.name + ":\n" + g.text + "\n")
	}
	return b.String()
}

func anyHasExt(files, exts []string) bool {
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		for _, e := range exts {
			if ext == e {
				return true
			}
		}
	}
	return false
}

// guideProbeFiles names one file of each language with guidance. The prompt
// fingerprint renders the prompt for them, so a change to any guidance is a
// cache miss rather than a replay of a review that was given the old text.
func guideProbeFiles() []string {
	files := make([]string, len(langGuides))
	for i, g := range langGuides {
		files[i] = "probe" + g.exts[0]
	}
	return files
}

// SystemPromptFor is the diff review's system prompt for files: the base
// prompt and the guidance for their languages.
func SystemPromptFor(files []string) string {
	return systemPrompt + languageGuidance(files)
}

// CodebaseSystemPromptFor is the codebase review's system prompt for files.
func CodebaseSystemPromptFor(files []string) string {
	return codebaseSystemPromptText + languageGuidance(files)
}

// SystemPromptWithRules is the diff review's system prompt with the rules
// pack's top-level rules in it. Everything every chunk shares comes first,
// in a fixed order (base prompt, rules, language guidance), and what is the
// chunk's own (its diff, the rule sets for its paths) comes in the user
// prompt after it, so a provider's prefix cache can serve the shared part.
func SystemPromptWithRules(files []string, rules *Rules) string {
	return systemPrompt + rulesPolicy(rules) + languageGuidance(files)
}

// CodebaseSystemPromptWithRules is SystemPromptWithRules for a codebase
// review.
func CodebaseSystemPromptWithRules(files []string, rules *Rules) string {
	return codebaseSystemPromptText + rulesPolicy(rules) + languageGuidance(files)
}

// rulesPolicy is the top-level rules as a system prompt section.
func rulesPolicy(rules *Rules) string {
	section := BuildRulesPromptSection(rules)
	if section == "" {
		return ""
	}
	return "\n\nReview policy for this repository:\n" + section
}
