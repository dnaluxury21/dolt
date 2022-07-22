// Copyright 2019 Dolthub, Inc.
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

package commands

import (
	"context"

	"github.com/fatih/color"

	"github.com/dolthub/dolt/go/cmd/dolt/cli"
	"github.com/dolthub/dolt/go/cmd/dolt/errhand"
	eventsapi "github.com/dolthub/dolt/go/gen/proto/dolt/services/eventsapi/v1alpha1"
	"github.com/dolthub/dolt/go/libraries/doltcore/env"
	"github.com/dolthub/dolt/go/libraries/doltcore/migrate"
	"github.com/dolthub/dolt/go/libraries/utils/argparser"
)

const (
	migrationPrompt = `Run "dolt migrate" to update this database to the latest data format`
	migrationMsg    = "Migrating database to the latest data format"

	migratePushFlag = "push"
	migratePullFlag = "pull"
)

var migrateDocs = cli.CommandDocumentationContent{
	ShortDesc: "Executes a database migration to use the latest Dolt data format.",
	LongDesc: `Migrate is a multi-purpose command to update the data format of a Dolt database. Over time, development 
on Dolt requires changes to the on-disk data format. These changes are necessary to improve Database performance and 
correctness. Migrating to the latest format is therefore necessary for compatibility with the latest Dolt clients, and
to take advantage of the newly released Dolt features.`,

	Synopsis: []string{
		"[ --push ] [ --pull ]",
	},
}

type MigrateCmd struct{}

// Name is returns the name of the Dolt cli command. This is what is used on the command line to invoke the command
func (cmd MigrateCmd) Name() string {
	return "migrate"
}

// Description returns a description of the command
func (cmd MigrateCmd) Description() string {
	return migrateDocs.ShortDesc
}

func (cmd MigrateCmd) Docs() *cli.CommandDocumentation {
	return nil
}

func (cmd MigrateCmd) ArgParser() *argparser.ArgParser {
	ap := argparser.NewArgParser()
	ap.SupportsFlag(migratePushFlag, "", "Push all migrated branches to the remote")
	ap.SupportsFlag(migratePullFlag, "", "Update all local tracking refs for a migrated remote")
	return ap
}

// EventType returns the type of the event to log
func (cmd MigrateCmd) EventType() eventsapi.ClientEventType {
	return eventsapi.ClientEventType_MIGRATE
}

// Exec executes the command
func (cmd MigrateCmd) Exec(ctx context.Context, commandStr string, args []string, dEnv *env.DoltEnv) int {
	ap := cmd.ArgParser()
	help, usage := cli.HelpAndUsagePrinters(cli.CommandDocsForCommandString(commandStr, migrateDocs, ap))
	apr := cli.ParseArgsOrDie(ap, args, help)

	if apr.Contains(migratePushFlag) && apr.Contains(migratePullFlag) {
		cli.PrintErrf(color.RedString("options --%s and --%s are mutually exclusive", migratePushFlag, migratePullFlag))
		return 1
	}

	if err := MigrateDatabase(ctx, dEnv); err != nil {
		verr := errhand.BuildDError("migration failed").AddCause(err).Build()
		return HandleVErrAndExitCode(verr, usage)
	}
	return 0
}

func MigrateDatabase(ctx context.Context, dEnv *env.DoltEnv) error {
	menv, err := migrate.NewEnvironment(ctx, dEnv)
	if err != nil {
		return err
	}
	p, err := menv.Migration.FS.Abs(".")
	if err != nil {
		return err
	}
	cli.Println("migrating database at tmp dir: ", p)

	err = migrate.TraverseDAG(ctx, menv.Existing.DoltDB, menv.Migration.DoltDB)
	if err != nil {
		return err
	}

	return migrate.SwapChunkStores(ctx, menv)
}
