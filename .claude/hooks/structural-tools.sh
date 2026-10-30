#!/usr/bin/env bash
# PreToolUse/Bash: refuse a byte tool where this repo has a structural one.
#
# A HABIT GUARD, NOT A SECURITY BOUNDARY. It catches the spellings agents
# actually reach for — `sed -i`, `sed -n 'N,Mp' file.go`, `grep … Makefile`,
# `cat > main.go <<EOF` — and points at the tool the docs/tooling rules name.
# Anyone determined to edit bytes can (`eval`, `xargs`, `perl -pi`, `cp`, a
# glob); chasing every spelling is a denylist that never converges, so it does
# not try. A false denial costs more than a missed bypass: it trains agents to
# route around the hook.
#
# The command is PARSED (`shfmt -ln zsh --to-json`; the Bash tool runs zsh), so
# pipes, lists, subshells, `$(…)`, `bash -c '…'`, quoting and line
# continuations are resolved rather than pattern-matched.
#
# What it judges:
#   * `sed` in place (-i, -I, --in-place, any cluster)     → deny (Edit/Write)
#   * a line-range read of a CODE file: `sed -n 'N,Mp'`,
#     `head`, `tail`, `awk 'NR…'`                          → deny (Read tool)
#   * a byte tool reading a Makefile / *.mk, as an operand
#     or via `<`                                           → deny (make -n)
#   * hand-written content (`cat`/`echo`/`printf`/`tee`, a
#     bare `> f`, a block) redirected into a repo source
#     file                                                 → deny (Edit/Write)
#   * a byte tool over Go or TypeScript sources            → reminder only
#
# Not judged: stdin reads (`make test | grep FAIL`), `git grep`, operands known
# only at run time (`cat "$f"`), tool output redirected anywhere (`go test >
# out.json` — generated output is not a hand edit), and relative paths after a
# `cd` in the same command (the directory is no longer known).
#
# Fails OPEN, with a notice in context, when jq or shfmt is missing or the
# command does not parse: a hook that blocks every command blocks the session.
#
# Exit 0 with no output = allowed. JSON on stdout carries a deny or a reminder.
set -o pipefail

payload="$(cat)"
if ! command -v jq >/dev/null 2>&1; then
    printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"structural-tools hook is INACTIVE: jq is not installed. Commands are not being checked."}}'
    exit 0
fi
cmd="$(printf '%s' "$payload" | jq -r '.tool_input.command // empty')"
[[ -z "$cmd" ]] && exit 0
CWD="$(printf '%s' "$payload" | jq -r '.cwd // empty')"
PROJECT="${CLAUDE_PROJECT_DIR:-$CWD}"
CWD="${CWD:-$PROJECT}"
CWD_UNKNOWN=0

denies=()
reminds=()
add_deny() {
    local r
    for r in "${denies[@]}"; do [[ "$r" == "$1" ]] && return; done
    denies+=("$1")
}
add_remind() {
    local r
    for r in "${reminds[@]}"; do [[ "$r" == "$1" ]] && return; done
    reminds+=("$1")
}

R_GO="A text search over Go. Fine for a literal sweep — but if the question is
semantic, docs/tooling/go.md routes it: 'go doc -u -short <pkg>' for shape, the
language server (LSP tool or gopls CLI) for meaning, 'gofmt -r' to rewrite. A
grep count is a candidate list, not an answer."
R_TS="A text search over TypeScript. The LSP tool (where your session has it)
sees structural satisfaction that grep cannot."
R_PARSE="structural-tools hook: this command did not parse as zsh, so it was NOT checked against docs/tooling rules."
R_NOSHFMT="structural-tools hook is INACTIVE: shfmt is not installed (brew install shfmt). Commands are not being checked."
D_SEDI="sed in place edits bytes in a file that has a grammar. CLAUDE.md: use
Edit/Write. At scale: gofmt -r or gopls rename for Go."
D_RANGE="A line-range read of a code file. Use the Read tool (offset/limit) — it
pages, it is cached, and it does not pay for comment blocks twice. Paging a
command's OUTPUT through a pipe, or a log file, is fine and is not refused."
D_MAKE="A byte tool on a Makefile. It hands back \$(VARIABLES) UNEXPANDED. Use:
  make -n <target>     what will actually run, fully expanded
GNU make EXECUTES a recipe line containing \$(MAKE) even under -n, so a
recursive target is not dry-run by -n — read that recipe with the Read tool."
D_WRITE="Hand-written content redirected into a source file in the repo.
CLAUDE.md: use Edit/Write. Tool output redirected to a file, and anything
written to your scratchpad, is fine."

is_makefile() {
    case "${1##*/}" in Makefile | makefile | GNUmakefile | *.mk) return 0 ;; esac
    return 1
}
is_code() {
    case "${1##*/}" in
    *.go | *.ts | *.tsx | *.mts | *.cts | *.js | *.jsx | *.mjs) return 0 ;;
    esac
    return 1
}
is_source() {
    is_code "$1" && return 0
    is_makefile "$1" && return 0
    case "${1##*/}" in
    go.mod | go.sum | *.json | *.yaml | *.yml | *.toml | *.md | *.sh | *.sql | \
        *.css | *.html | Dockerfile | .env | .env.*) return 0 ;;
    esac
    return 1
}
lang_of() { # go | ts | ""
    case "$1" in
    *.go | backend | backend/*) echo go ;;
    *.ts | *.tsx | *.mts | *.cts) echo ts ;;
    esac
}
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
# in_repo: is this path inside the project? Case-insensitive, because APFS is.
in_repo() {
    local p="$1"
    [[ -z "$PROJECT" ]] && return 1
    case "$p" in
    "~" | "~/"*) p="$HOME${p#\~}" ;;
    /*) ;;
    *) ((CWD_UNKNOWN == 1)) && return 1; p="$CWD/$p" ;;
    esac
    local lp lr
    lp="$(lower "$p")"
    lr="$(lower "$PROJECT")"
    [[ "$lp" == "$lr" || "$lp" == "$lr"/* ]]
}

# \001 in a word marks a part only known at run time ($x, $(…)); globs stay
# literal. Such an operand is never judged.
DYN=$'\001'

# Parse one command string; emit one JSON object per statement that is a
# simple command or carries redirections (a block, a loop, a bare `> f`).
parse() {
    printf '%s' "$1" | shfmt -ln zsh --to-json 2>/dev/null | jq -c '
      def unesc: gsub("\\\\(?<c>.)"; "\(.c)");
      def part: if .Type=="Lit" then (.Value|unesc)
        elif .Type=="SglQuoted" then .Value
        elif .Type=="DblQuoted" then ([(.Parts//[])[]|part] | if any(.==null) then null else join("") end)
        else null end;
      def word: [(.Parts//[])[]|part] | if any(.==null) then null else join("") end;
      .. | objects | select((.Cmd?.Type?=="CallExpr") or ((.Redirs//[]) | length > 0))
      | {call: (.Cmd?.Type?=="CallExpr"),
         args: [(.Cmd?.Args? // [])[]|word],
         redirs: [(.Redirs//[])[]|{op: .Op, w: (.Word|word)}]}'
}

# judge <json-of-one-statement>
judge() {
    local js="$1" a args=() i w op
    while IFS= read -r -d '' a; do args+=("$a"); done < <(printf '%s' "$js" | jq -j --arg d "$DYN" '.args[] | ((. // $d) + "\u0000")')
    local outs=() inputs=()
    while IFS=$'\t' read -r op w; do
        [[ -z "$op" ]] && continue
        case "$op" in
        '>' | '>>' | '>|' | '&>' | '&>>') outs+=("$w") ;;
        '<') inputs+=("$w") ;;
        esac
    done < <(printf '%s' "$js" | jq -r --arg d "$DYN" '.redirs[] | [.op, (.w // $d)] | @tsv')

    # Unwrap command prefixes and their options.
    while [[ ${#args[@]} -gt 0 ]]; do
        local t="${args[0]##*/}"
        case "$t" in
        sudo | doas | env | time | command | builtin | exec | nice | nohup | noglob)
            args=("${args[@]:1}")
            while [[ ${#args[@]} -gt 0 ]]; do
                case "$t:${args[0]}" in
                *:--) args=("${args[@]:1}"); break ;;
                sudo:-[ugCphUD] | doas:-[uC] | nice:-n | env:-[uSP]) args=("${args[@]:2}") ;;
                *:-*) args=("${args[@]:1}") ;;
                env:*=*) args=("${args[@]:1}") ;;
                *) break ;;
                esac
            done
            ;;
        *) break ;;
        esac
    done
    local tool=""
    if [[ ${#args[@]} -gt 0 ]]; then
        tool="${args[0]##*/}"
        args=("${args[@]:1}")
    fi

    case "$tool" in
    cd | pushd | popd) CWD_UNKNOWN=1; return ;;
    esac

    # Hand-written content into a source file: cat/echo/printf/print/tee, a
    # bare redirection, or a block/loop.
    local f
    case "$tool" in
    "" | cat | echo | printf | print)
        for f in ${outs[@]+"${outs[@]}"}; do
            in_repo "$f" && is_source "$f" && add_deny "$D_WRITE"
        done
        ;;
    tee)
        for f in ${args[@]+"${args[@]}"}; do
            [[ "$f" == -* || "$f" == *"$DYN"* ]] && continue
            in_repo "$f" && is_source "$f" && add_deny "$D_WRITE"
        done
        return
        ;;
    esac

    # A bare `< Makefile` (zsh pages it) or a block reading one.
    if [[ -z "$tool" ]]; then
        for f in ${inputs[@]+"${inputs[@]}"}; do is_makefile "$f" && add_deny "$D_MAKE"; done
        return
    fi

    # A shell given a script string: judge the script.
    case "$tool" in
    bash | sh | zsh | dash | ksh)
        for ((i = 0; i < ${#args[@]}; i++)); do
            [[ "${args[i]}" == -- ]] && continue
            if [[ "${args[i]}" =~ ^-[a-zA-Z]*c[a-zA-Z]*$ ]]; then
                local j=$((i + 1))
                [[ "${args[j]:-}" == -- ]] && j=$((j + 1))
                [[ -n "${args[j]:-}" ]] && analyze "${args[j]}"
                return
            fi
        done
        return
        ;;
    grep | egrep | fgrep | rg | sed | gsed | awk | gawk | cat | head | tail | nl | less | more | bat) ;;
    *) return ;;
    esac

    # Options that consume a value, per tool: short letters, long names.
    local sv="" lv="" positional_script=1
    case "$tool" in
    grep | egrep | fgrep) sv="efmABCDd" lv="regexp file max-count after-context before-context context include exclude exclude-from exclude-dir label devices directories binary-files group-separator" ;;
    rg) sv="efgmABCtTjMErd" lv="regexp file glob iglob max-count after-context before-context context type type-not type-add threads max-columns encoding replace max-depth sort sortr colors color pre pre-glob ignore-file max-filesize path-separator context-separator engine" ;;
    sed | gsed) sv="efl" lv="expression file line-length" ;;
    awk | gawk) sv="fvF" lv="file assign field-separator" ;;
    head | tail) sv="nc" lv="lines bytes" positional_script=0 ;;
    bat) sv="lrHm" lv="language line-range highlight-line map-syntax style theme tabs wrap terminal-width pager decorations color" positional_script=0 ;;
    *) positional_script=0 ;;
    esac

    local files=() scripts=() inplace=0 explicit_script=0 endopts=0 n=${#args[@]}
    for ((i = 0; i < n; i++)); do
        a="${args[i]}"
        if ((endopts == 0)) && [[ "$a" == -- ]]; then endopts=1; continue; fi
        if ((endopts == 0)) && [[ "$a" == --* ]]; then
            local name="${a#--}" val=""
            [[ "$name" == *=* ]] && { val="${name#*=}"; name="${name%%=*}"; }
            [[ "$name" == in-place ]] && inplace=1
            if [[ " $lv " == *" $name "* ]]; then
                [[ "$a" != *=* ]] && { ((i++)); val="${args[i]:-}"; }
                case "$name" in regexp | expression | file) explicit_script=1; scripts+=("$val") ;; esac
            fi
            continue
        fi
        if ((endopts == 0)) && [[ "$a" == -?* ]]; then
            local k c
            for ((k = 1; k < ${#a}; k++)); do
                c="${a:k:1}"
                # -i (GNU and BSD) and -I (BSD) both edit in place.
                if [[ ("$tool" == sed || "$tool" == gsed) && ("$c" == i || "$c" == I) ]]; then inplace=1; break; fi
                if [[ "$sv" == *"$c"* ]]; then
                    local v="${a:k+1}"
                    [[ -z "$v" ]] && { ((i++)); v="${args[i]:-}"; }
                    [[ "$c" == e || "$c" == f ]] && [[ "$tool" != head && "$tool" != tail ]] && { explicit_script=1; scripts+=("$v"); }
                    break
                fi
            done
            continue
        fi
        if ((positional_script == 1 && explicit_script == 0 && ${#scripts[@]} == 0)); then
            scripts+=("$a")
            continue
        fi
        files+=("$a")
    done
    files+=(${inputs[@]+"${inputs[@]}"})

    ((inplace == 1)) && { add_deny "$D_SEDI"; return; }

    local known=() code=0
    for f in ${files[@]+"${files[@]}"}; do
        [[ "$f" != *"$DYN"* && "$f" != - ]] || continue
        known+=("$f")
        is_code "$f" && code=1
    done
    [[ ${#known[@]} -eq 0 ]] && return

    for f in "${known[@]}"; do
        is_makefile "$f" && add_deny "$D_MAKE"
    done

    local s
    if ((code == 1)); then
        case "$tool" in
        sed | gsed)
            for s in ${scripts[@]+"${scripts[@]}"}; do
                [[ "$s" =~ ^[[:space:]]*([0-9]+|\$)([[:space:]]*,[[:space:]]*([0-9]+|\$|\+[0-9]+))?[[:space:]]*[pq][[:space:]]*$ ]] && add_deny "$D_RANGE"
            done
            ;;
        head | tail) add_deny "$D_RANGE" ;;
        awk | gawk)
            for s in ${scripts[@]+"${scripts[@]}"}; do
                [[ "$s" =~ (^|[^A-Za-z_])F?NR[[:space:]]*(==|[<>]=?) ]] && add_deny "$D_RANGE"
            done
            ;;
        esac
    fi

    for f in "${known[@]}"; do
        case "$(lang_of "$f")" in
        go) add_remind "$R_GO" ;;
        ts) add_remind "$R_TS" ;;
        esac
    done
}

# analyze <command-string>. Recursion through `bash -c '…'` terminates: each
# level's script is a strict substring of the one above.
analyze() {
    local line out
    if ! out="$(parse "$1")"; then
        add_remind "$R_PARSE"
        return
    fi
    while IFS= read -r line; do
        [[ -n "$line" ]] && judge "$line"
    done <<<"$out"
}

if ! command -v shfmt >/dev/null 2>&1; then
    add_remind "$R_NOSHFMT"
else
    analyze "$cmd"
fi

if [[ ${#denies[@]} -gt 0 ]]; then
    reason="$(printf '%s\n\n' "${denies[@]}")"
    jq -n --arg r "$reason" '{hookSpecificOutput: {hookEventName: "PreToolUse",
        permissionDecision: "deny", permissionDecisionReason: $r}}'
elif [[ ${#reminds[@]} -gt 0 ]]; then
    ctx="$(printf '%s\n\n' "${reminds[@]}")"
    jq -n --arg c "$ctx" '{hookSpecificOutput: {hookEventName: "PreToolUse",
        additionalContext: $c}}'
fi
exit 0
