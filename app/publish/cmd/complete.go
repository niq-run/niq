// Shell completion: `niq completion <shell>` prints a script that completes
// subcommands, flags, and — the part that actually saves typing — the project
// ids for `niq project run`.
//
// The generated scripts call `niq project list --ids`, so completion always
// reflects the projects on this machine without a separate cache to keep fresh.
package main

import (
	"fmt"
)

func runCompletion(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: niq completion bash|zsh|fish")
	}
	switch args[0] {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	case "help", "-h", "--help":
		fmt.Println(bold("usage"))
		printUsageRows([][2]string{
			{"niq completion bash", "source <(niq completion bash)"},
			{"niq completion zsh", "niq completion zsh > \"${fpath[1]}/_niq\""},
			{"niq completion fish", "niq completion fish | source"},
		})
	default:
		return fmt.Errorf("unknown shell %q (want bash, zsh or fish)", args[0])
	}
	return nil
}

const bashCompletion = `# niq completion for bash — source <(niq completion bash)
_niq() {
	local cur
	cur="${COMP_WORDS[COMP_CWORD]}"
	COMPREPLY=()

	if (( COMP_CWORD == 1 )); then
		COMPREPLY=( $(compgen -W "start stop restart status project completion help version" -- "$cur") )
		return
	fi

	case "${COMP_WORDS[1]}" in
	project)
		if (( COMP_CWORD == 2 )); then
			COMPREPLY=( $(compgen -W "list create run help" -- "$cur") )
			return
		fi
		case "${COMP_WORDS[2]}" in
		run)
			if [[ "$cur" == -* ]]; then
				COMPREPLY=( $(compgen -W "--bus --webui --webui-auth" -- "$cur") )
			else
				COMPREPLY=( $(compgen -W "$(niq project list --ids 2>/dev/null)" -- "$cur") )
			fi
			;;
		list)
			COMPREPLY=( $(compgen -W "--ids" -- "$cur") )
			;;
		create)
			COMPREPLY=( $(compgen -W "--template" -- "$cur") )
			;;
		esac
		;;
	stop)
		COMPREPLY=( $(compgen -W "--all" -- "$cur") )
		;;
	restart)
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

_niq_projects() {
	local -a ids
	ids=(${(f)"$(niq project list --ids 2>/dev/null)"})
	_describe -t projects 'project' ids
}

_niq() {
	local -a commands
	commands=(
		'start:start the control plane in the background'
		'stop:stop the control plane'
		'restart:restart the control plane'
		'status:show the control plane and its projects'
		'project:pick a project, or create/run one'
		'completion:print a completion script'
		'help:print usage'
		'version:print the version'
	)
	if (( CURRENT == 2 )); then
		_describe -t commands 'niq command' commands
		return
	fi

	case $words[2] in
	project)
		if (( CURRENT == 3 )); then
			local -a sub
			sub=(
				'list:pick or list projects'
				'create:create a project from a template'
				'run:run an instance in this terminal'
			)
			_describe -t commands 'niq project' sub
			return
		fi
		case $words[3] in
		run)
			if [[ $PREFIX == -* ]]; then
				_values 'option' '--bus[event bus address]' '--webui[WebUI address]' '--webui-auth[basic auth user:pass]'
			else
				_niq_projects
			fi
			;;
		list)
			_values 'option' '--ids[print project ids only]'
			;;
		create)
			_values 'option' '--template[template name]'
			;;
		esac
		;;
	stop)
		_values 'option' '--all[also stop the project instances]'
		;;
	restart)
		_values 'option' '-p[also restart the running projects]' '--projects[also restart the running projects]'
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

complete -c niq -n '__fish_use_subcommand' -a start -d 'start the control plane in the background'
complete -c niq -n '__fish_use_subcommand' -a stop -d 'stop the control plane'
complete -c niq -n '__fish_use_subcommand' -a restart -d 'restart the control plane'
complete -c niq -n '__fish_use_subcommand' -a status -d 'show the control plane and its projects'
complete -c niq -n '__fish_use_subcommand' -a project -d 'pick a project, or create/run one'
complete -c niq -n '__fish_use_subcommand' -a completion -d 'print a completion script'
complete -c niq -n '__fish_use_subcommand' -a help -d 'print usage'
complete -c niq -n '__fish_use_subcommand' -a version -d 'print the version'

complete -c niq -n '__fish_seen_subcommand_from project; and not __fish_seen_subcommand_from list create run' -a list -d 'pick or list projects'
complete -c niq -n '__fish_seen_subcommand_from project; and not __fish_seen_subcommand_from list create run' -a create -d 'create a project from a template'
complete -c niq -n '__fish_seen_subcommand_from project; and not __fish_seen_subcommand_from list create run' -a run -d 'run an instance in this terminal'

complete -c niq -n '__fish_seen_subcommand_from project; and __fish_seen_subcommand_from run' -a '(niq project list --ids 2>/dev/null)'
complete -c niq -n '__fish_seen_subcommand_from project; and __fish_seen_subcommand_from list' -l ids -d 'print project ids only'
complete -c niq -n '__fish_seen_subcommand_from project; and __fish_seen_subcommand_from create' -l template -d 'template name'
complete -c niq -n '__fish_seen_subcommand_from project; and __fish_seen_subcommand_from run' -l bus -d 'event bus address'
complete -c niq -n '__fish_seen_subcommand_from project; and __fish_seen_subcommand_from run' -l webui -d 'WebUI address'
complete -c niq -n '__fish_seen_subcommand_from project; and __fish_seen_subcommand_from run' -l webui-auth -d 'basic auth user:pass'

complete -c niq -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
complete -c niq -n '__fish_seen_subcommand_from stop' -l all -d 'also stop the project instances'
complete -c niq -n '__fish_seen_subcommand_from restart' -l projects -s p -d 'also restart the running projects'
complete -c niq -l addr -d 'listen address'
complete -c niq -l auth -d 'basic auth user:pass'
complete -c niq -l foreground -s f -d 'run attached to this terminal'
`
