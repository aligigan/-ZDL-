# bash completion for zdl
# install: source this file, or copy to /usr/share/bash-completion/completions/zdl

_zdl() {
    local cur prev cmd
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    cmd="${COMP_WORDS[1]}"

    local commands="ui get resume clip list info clean version help"

    if [[ $COMP_CWORD -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
        return 0
    fi

    local common_flags="-o --output -d --dir -x --connections --retries --cookie --cookies-from-zen --zen-profile --referer --user-agent --header --rate --sha256 --insecure -y --yes --force --restart -q --quiet --verbose --notify"
    local ui_flags="-d --dir -x --connections --retries --cookie --cookies-from-zen --zen-profile --referer --user-agent --header --rate --insecure -y --yes -q --quiet --verbose --notify"

    case "$prev" in
        -o|--output|-d|--dir|--zen-profile)
            COMPREPLY=( $(compgen -f -- "$cur") )
            return 0
            ;;
        --header)
            return 0
            ;;
        resume|info|clean)
            COMPREPLY=( $(compgen -f -- "$cur") )
            return 0
            ;;
    esac

    case "$cmd" in
        ui)
            COMPREPLY=( $(compgen -W "$ui_flags" -- "$cur") )
            ;;
        get)
            COMPREPLY=( $(compgen -W "$common_flags" -- "$cur") $(compgen -f -- "$cur") )
            ;;
        resume)
            COMPREPLY=( $(compgen -W "$common_flags --url" -- "$cur") $(compgen -f -- "$cur") )
            ;;
        clip)
            COMPREPLY=( $(compgen -W "$common_flags --auto" -- "$cur") )
            ;;
        list)
            COMPREPLY=( $(compgen -W "-d --dir" -- "$cur") )
            ;;
        *)
            COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
            ;;
    esac
}

complete -F _zdl zdl
