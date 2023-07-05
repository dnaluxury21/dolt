// Copyright 2023 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dsess

import (
	"context"
	"math"
	"strings"
	"sync"

	"github.com/dolthub/dolt/go/libraries/doltcore/doltdb"
	"github.com/dolthub/dolt/go/libraries/doltcore/ref"
	"github.com/dolthub/dolt/go/libraries/doltcore/schema"
	"github.com/dolthub/dolt/go/libraries/doltcore/sqle/globalstate"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

type AutoIncrementTracker struct {
	dbName    string
	sequences map[string]uint64
	mu        *sync.Mutex
}

var _ globalstate.AutoIncrementTracker = AutoIncrementTracker{}

// NewAutoIncrementTracker returns a new autoincrement tracker for the roots given. All roots sets must be
// considered because the auto increment value for a table is tracked globally, across all branches.
// Roots provided should be the working sets when available, or the branches when they are not (e.g. for remote
// branches that don't have a local working set)
func NewAutoIncrementTracker(ctx context.Context, dbName string, roots ...doltdb.Rootish) (AutoIncrementTracker, error) {
	ait := AutoIncrementTracker{
		dbName:    dbName,
		sequences: make(map[string]uint64),
		mu:        &sync.Mutex{},
	}

	for _, root := range roots {
		root, err := root.ResolveRootValue(ctx)
		if err != nil {
			return AutoIncrementTracker{}, err
		}

		err = root.IterTables(ctx, func(tableName string, table *doltdb.Table, sch schema.Schema) (bool, error) {
			ok := schema.HasAutoIncrement(sch)
			if !ok {
				return false, nil
			}

			tableName = strings.ToLower(tableName)

			seq, err := table.GetAutoIncrementValue(ctx)
			if err != nil {
				return true, err
			}

			if seq > ait.sequences[tableName] {
				ait.sequences[tableName] = seq
			}

			return false, nil
		})

		if err != nil {
			return AutoIncrementTracker{}, err
		}
	}

	return ait, nil
}

// Current returns the next value to be generated in the auto increment sequence for the table named
func (a AutoIncrementTracker) Current(tableName string) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sequences[strings.ToLower(tableName)]
}

// Next returns the next auto increment value for the table named using the provided value from an insert (which may
// be null or 0, in which case it will be generated from the sequence).
func (a AutoIncrementTracker) Next(tbl string, insertVal interface{}) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	tbl = strings.ToLower(tbl)

	given, err := CoerceAutoIncrementValue(insertVal)
	if err != nil {
		return 0, err
	}

	curr := a.sequences[tbl]

	if given == 0 {
		// |given| is 0 or NULL
		a.sequences[tbl]++
		return curr, nil
	}

	if given >= curr {
		a.sequences[tbl] = given
		a.sequences[tbl]++
		return given, nil
	}

	// |given| < curr
	return given, nil
}

func (a AutoIncrementTracker) CoerceAutoIncrementValue(val interface{}) (uint64, error) {
	return CoerceAutoIncrementValue(val)
}

// CoerceAutoIncrementValue converts |val| into an AUTO_INCREMENT sequence value
func CoerceAutoIncrementValue(val interface{}) (uint64, error) {
	switch typ := val.(type) {
	case float32:
		val = math.Round(float64(typ))
	case float64:
		val = math.Round(typ)
	}

	var err error
	val, _, err = types.Uint64.Convert(val)
	if err != nil {
		return 0, err
	}
	if val == nil || val == uint64(0) {
		return 0, nil
	}
	return val.(uint64), nil
}

// Set sets the auto increment value for the table named, if it's greater than the one already registered for this
// table. Otherwise, the update is silently disregarded. So far this matches the MySQL behavior, but Dolt uses the
// maximum value for this table across all branches.
func (a AutoIncrementTracker) Set(ctx *sql.Context, ws ref.WorkingSetRef, tableName string, newAutoIncVal uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	tableName = strings.ToLower(tableName)

	existing := a.sequences[tableName]
	if newAutoIncVal > existing {
		a.sequences[strings.ToLower(tableName)] = newAutoIncVal
		return nil
	} else {
		// re-establish our baseline for this table on all branches before making our decision 
		return a.deepSet(ctx, ws, tableName, newAutoIncVal)
	}
}

// deepSet sets the auto increment value for the table named, if it's greater than the one on any branch head for this 
// database, ignoring the current in-memory tracker value
func (a AutoIncrementTracker) deepSet(ctx *sql.Context, ws ref.WorkingSetRef, tableName string, newAutoIncVal uint64) error {
	sess := DSessFromSess(ctx.Session)
	db, ok := sess.Provider().BaseDatabase(ctx, a.dbName)

	// just give up if we can't find this db for any reason, or it's a non-versioned DB
	if !ok || !db.Versioned() {
		return nil
	}

	maxAutoInc := newAutoIncVal
	doltdbs := db.DoltDatabases()
	for _, db := range doltdbs {
		branches, err := db.GetBranches(ctx)
		if err != nil {
			return err
		}

		remotes, err := db.GetRemoteRefs(ctx)
		if err != nil {
			return err
		}

		rootRefs := make([]ref.DoltRef, 0, len(branches)+len(remotes))
		rootRefs = append(rootRefs, branches...)
		rootRefs = append(rootRefs, remotes...)
		
		for _, b := range rootRefs {
			var rootish doltdb.Rootish
			switch b.GetType() {
			case ref.BranchRefType:
				wsRef, err := ref.WorkingSetRefForHead(b)
				if err != nil {
					return err
				}
				
				if wsRef == ws {
					// we don't need to check the working set we're updating
					continue
				}

				ws, err := db.ResolveWorkingSet(ctx, wsRef)
				if err == doltdb.ErrWorkingSetNotFound {
					// use the branch head if there isn't a working set for it
					cm, err := db.ResolveCommitRef(ctx, b)
					if err != nil {
						return err
					}
					rootish = cm
				} else if err != nil {
					return err
				} else {
					rootish = ws
				}
			case ref.RemoteRefType:
				cm, err := db.ResolveCommitRef(ctx, b)
				if err != nil {
					return err
				}
				rootish = cm
			}

			root, err := rootish.ResolveRootValue(ctx)
			if err != nil {
				return err
			}

			table, _, ok, err := root.GetTableInsensitive(ctx, tableName)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}

			sch, err := table.GetSchema(ctx)
			if err != nil {
				return err
			}

			if !schema.HasAutoIncrement(sch) {
				continue
			}

			tableName = strings.ToLower(tableName)
			seq, err := table.GetAutoIncrementValue(ctx)
			if err != nil {
				return err
			}
			
			if seq > maxAutoInc {
				maxAutoInc = seq
			}
		}
	}

	// If we made it through the above loop, that means there is no value on any branch higher than the one given, 
	// so we can set it
	a.sequences[tableName] = maxAutoInc
	return nil
}

// AddNewTable initializes a new table with an auto increment column to the tracker, as necessary
func (a AutoIncrementTracker) AddNewTable(tableName string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	tableName = strings.ToLower(tableName)
	// only initialize the sequence for this table if no other branch has such a table
	if _, ok := a.sequences[tableName]; !ok {
		a.sequences[tableName] = uint64(1)
	}
}

// DropTable drops the table with the name given.
// To establish the new auto increment value, callers must also pass all other working sets in scope that may include
// a table with the same name, omitting the working set that just deleted the table named.
func (a AutoIncrementTracker) DropTable(ctx *sql.Context, tableName string, wses ...*doltdb.WorkingSet) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	tableName = strings.ToLower(tableName)

	// reset sequence to the minimum value
	a.sequences[tableName] = 1

	// Get the new highest value from all tables in the working sets given
	for _, ws := range wses {
		table, _, exists, err := ws.WorkingRoot().GetTableInsensitive(ctx, tableName)
		if err != nil {
			return err
		}

		if !exists {
			continue
		}

		sch, err := table.GetSchema(ctx)
		if err != nil {
			return err
		}

		if schema.HasAutoIncrement(sch) {
			seq, err := table.GetAutoIncrementValue(ctx)
			if err != nil {
				return err
			}

			if seq > a.sequences[tableName] {
				a.sequences[tableName] = seq
			}
		}
	}

	return nil
}
