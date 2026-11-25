// `niq c [project]` opens the terminal conversation for one project.
//
// It is the same client contract as the picker: resolve the gateway, then talk
// to the project through it. With no project named it uses the only one, and
// asks for an id rather than guessing when there are several.
package cli

import (
	"flag"
	"fmt"

	"github.com/niq-run/niq/app/client/tui"
	"github.com/niq-run/niq/app/standalone/contract"
)

func runChat(args []string) error {
	fs := flag.NewFlagSet("niq c", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Println(bold("niq c [project]") + dim(" — chat with a project in the terminal"))
		fmt.Println()
		fmt.Println(dim("With no project, the only project is used; with several, name one."))
	}
	if err := fs.Parse(args); err != nil {
		return helpOrErr(err)
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: niq c [project]")
	}

	baseURL, err := ensureGatewayForUI()
	if err != nil {
		return err
	}

	id := fs.Arg(0)
	if id == "" {
		views, err := contract.New(baseURL).List()
		if err != nil {
			return err
		}
		switch len(views) {
		case 0:
			return fmt.Errorf("no projects yet — create one in the web UI: niq w")
		case 1:
			id = views[0].ID
		default:
			return fmt.Errorf("choose a project: niq c <project>  (list them with: niq status --ids)")
		}
	}
	return tui.RunChat(baseURL, id)
}
