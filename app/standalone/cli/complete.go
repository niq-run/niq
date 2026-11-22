// Shell completion: `niq completion <shell>` prints a script that completes
// subcommands and their flags.
//
// Projects are not a verb any more — every UI reaches them through a running
// gateway — so the scripts complete the gateway commands only. `niq status
// --ids` remains, for scripts that want the project ids.
package cli

import (
	"fmt"
)

func runCompletion(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: niq completion install|bash|zsh|fish")
	}
	switch args[0] {
	case "install":
		return runCompletionInstall(args[1:])
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	case "help", "-h", "--help":
		fmt.Println(bracketed("usage"))
		printUsageRows([][2]string{
			{"niq completion install", "write it where your shell already looks (no dotfile edit)"},
			{"niq completion bash", "source <(niq completion bash)"},
			{"niq completion zsh", "niq completion zsh > \"${fpath[1]}/_niq\""},
			{"niq completion fish", "niq completion fish | source"},
		})
	default:
		return fmt.Errorf("unknown target %q (want install, bash, zsh or fish)", args[0])
	}
	return nil
}

const bashCompletion = `# niq completion for bash — source <(niq completion bash)
_niq() {
	local cur
	cur="${COMP_WORDS[COMP_CWORD]}"
	COMPREPLY=()

	if (( COMP_CWORD == 1 )); then
		COMPREPLY=( $(compgen -W "start stop restart status update i w completion help version" -- "$cur") )
		return
	fi

	case "${COMP_WORDS[1]}" in
	stop|restart)
		COMPREPLY=( $(compgen -W "-p --projects" -- "$cur") )
		;;
	completion|complete)
		COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") )
		;;
	*)
		if [[ "$cur" == -* ]]; then
			COMPREPLY=( $(compgen -W "--addr --auth --foreground -f" -- "$cur") )
		fi
		;;
	esac
}
complete -F _niq niq
`

const zshCompletion = `#compdef niq
# niq completion for zsh — niq completion zsh > "${fpath[1]}/_niq"

_niq() {
	local -a commands
	commands=(
		'w:open the browser UI, starting the gateway if needed'
		'i:open the interactive terminal UI'
		'stop:stop the gateway'
		'restart:restart the gateway'
		'status:show the gateway and its projects'
		'update:install the latest release with npm'
		'completion:print a completion script'
		'help:print usage'
		'version:print the version'
	)
	if (( CURRENT == 2 )); then
		_describe -t commands 'niq command' commands
		return
	fi

	case $words[2] in
	stop|restart)
		_values 'option' '-p[also act on the running projects]' '--projects[also act on the running projects]'
		;;
	status)
		_values 'option' '--json[machine-readable snapshot]' '--ids[print project ids only]'
		;;
	w)
		_values 'option' '--addr[listen address]' '--auth[basic auth user:pass]' '--foreground[run attached to this terminal]'
		;;
	completion|complete)
		_values 'shell' bash zsh fish
		;;
	*)
		_values 'option' '--addr[listen address]' '--auth[basic auth user:pass]' '--foreground[run attached to this terminal]'
		;;
	esac
}

_niq "$@"
`

const fishCompletion = `# niq completion for fish — niq completion fish | source
complete -c niq -f

complete -c niq -n '__fish_use_subcommand' -a w -d 'open the browser UI, starting the gateway if needed'
complete -c niq -n '__fish_use_subcommand' -a i -d 'open the interactive terminal UI'
complete -c niq -n '__fish_use_subcommand' -a stop -d 'stop the gateway'
complete -c niq -n '__fish_use_subcommand' -a restart -d 'restart the gateway'
complete -c niq -n '__fish_use_subcommand' -a status -d 'show the gateway and its projects'
complete -c niq -n '__fish_use_subcommand' -a update -d 'install the latest release with npm'
complete -c niq -n '__fish_use_subcommand' -a completion -d 'print a completion script'
complete -c niq -n '__fish_use_subcommand' -a help -d 'print usage'
complete -c niq -n '__fish_use_subcommand' -a version -d 'print the version'

complete -c niq -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
complete -c niq -n '__fish_seen_subcommand_from stop' -l projects -s p -d 'also stop the running projects'
complete -c niq -n '__fish_seen_subcommand_from restart' -l projects -s p -d 'also restart the running projects'
complete -c niq -l addr -d 'listen address'
complete -c niq -l auth -d 'basic auth user:pass'
complete -c niq -l foreground -s f -d 'run attached to this terminal'
`
