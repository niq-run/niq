// project.json as the worker lifecycle's declaration store: the project owns
// where declarations live and how they are written, while the lifecycle
// (app/project/worker) only sees the DeclStore interface.
package project

import (
	"fmt"

	projworker "github.com/niq-run/niq/app/project/worker"
)

// projectDeclStore implements worker.DeclStore over one project's active
// workers.json. Every call re-reads the file: declarations are edited by the
// WebUI, by a runtime spawn and by this store, and a cached copy would let one
// writer silently revert another.
type projectDeclStore struct {
	projectID string
}

var _ projworker.DeclStore = projectDeclStore{}

func (s projectDeclStore) Get(id string) (*projworker.Declaration, bool, error) {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return nil, false, err
	}
	d, ok := FindWorker(set, id)
	if !ok {
		return nil, false, nil
	}
	return &d, true, nil
}

func (s projectDeclStore) List() ([]*projworker.Declaration, error) {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return nil, err
	}
	out := make([]*projworker.Declaration, 0, len(set.Workers))
	for i := range set.Workers {
		d := set.Workers[i]
		out = append(out, &d)
	}
	return out, nil
}

func (s projectDeclStore) Add(d *projworker.Declaration) error {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return err
	}
	if _, ok := FindWorker(set, d.ID); ok {
		return fmt.Errorf("worker %s already exists", d.ID)
	}
	set.Workers = append(set.Workers, *d)
	return SaveWorkers(s.projectID, set)
}

func (s projectDeclStore) Update(d *projworker.Declaration) error {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return err
	}
	for i := range set.Workers {
		if set.Workers[i].ID == d.ID {
			set.Workers[i] = *d
			return SaveWorkers(s.projectID, set)
		}
	}
	return fmt.Errorf("worker %s is not declared", d.ID)
}

func (s projectDeclStore) Delete(id string) error {
	return RemoveWorkerDecl(s.projectID, id)
}

func (s projectDeclStore) SetCredential(id, cred string) error {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return err
	}
	for i := range set.Workers {
		if set.Workers[i].ID == id {
			set.Workers[i].Credential = cred
			return SaveWorkers(s.projectID, set)
		}
	}
	return fmt.Errorf("worker %s is not declared", id)
}

func (s projectDeclStore) RemoveState(id string) error {
	return removeWorkerStateDir(s.projectID, id)
}
