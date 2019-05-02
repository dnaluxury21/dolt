package env

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/attic-labs/noms/go/hash"
	"github.com/attic-labs/noms/go/spec"
	"github.com/attic-labs/noms/go/types"
	"github.com/liquidata-inc/ld/dolt/go/libraries/doltcore/creds"
	"github.com/liquidata-inc/ld/dolt/go/libraries/doltcore/doltdb"
	"github.com/liquidata-inc/ld/dolt/go/libraries/doltcore/ref"
	"github.com/liquidata-inc/ld/dolt/go/libraries/doltcore/schema"
	"github.com/liquidata-inc/ld/dolt/go/libraries/doltcore/schema/encoding"
	"github.com/liquidata-inc/ld/dolt/go/libraries/utils/filesys"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"path/filepath"
	"strings"
)

const (
	DefaultLoginUrl       = "https://dolthub.awsdev.ld-corp.com/settings/credentials"
	DefaultRemotesApiHost = "dolthub.com"
	DefaultRemotesApiPort = "8080"
)

var ErrPreexistingDoltDir = errors.New(".dolt dir already exists")
var ErrStateUpdate = errors.New("error updating local data repo state")
var ErrMarshallingSchema = errors.New("error marshalling schema")
var ErrInvalidCredsFile = errors.New("invalid creds file")

// DoltEnv holds the state of the current environment used by the cli.
type DoltEnv struct {
	Config     *DoltCliConfig
	CfgLoadErr error

	RepoState *RepoState
	RSLoadErr error

	DoltDB *doltdb.DoltDB

	FS  filesys.Filesys
	loc doltdb.Location
	hdp HomeDirProvider
}

// Load loads the DoltEnv for the current directory of the cli
func Load(ctx context.Context, hdp HomeDirProvider, fs filesys.Filesys, loc doltdb.Location) *DoltEnv {
	config, cfgErr := loadDoltCliConfig(hdp, fs)
	repoState, rsErr := LoadRepoState(fs)
	ddb := doltdb.LoadDoltDB(ctx, loc)

	dEnv := &DoltEnv{
		config,
		cfgErr,
		repoState,
		rsErr,
		ddb,
		fs,
		loc,
		hdp,
	}

	spec.ExternalProtocols[DoltNomsProtocolID] = &DoltProtocol{dEnv}

	return dEnv
}

// HasDoltDir returns true if the .dolt directory exists and is a valid directory
func (dEnv *DoltEnv) HasDoltDir() bool {
	return dEnv.hasDoltDir("./")
}

func (dEnv *DoltEnv) HasDoltDataDir() bool {
	return dEnv.hasDoltDataDir("./")
}

func (dEnv *DoltEnv) hasDoltDir(path string) bool {
	exists, isDir := dEnv.FS.Exists(filepath.Join(path, doltdb.DoltDir))
	return exists && isDir
}

func (dEnv *DoltEnv) hasDoltDataDir(path string) bool {
	exists, isDir := dEnv.FS.Exists(filepath.Join(path, doltdb.DoltDataDir))
	return exists && isDir
}

// HasLocalConfig returns true if a repository local config file
func (dEnv *DoltEnv) HasLocalConfig() bool {
	_, ok := dEnv.Config.GetConfig(LocalConfig)

	return ok
}

func (dEnv *DoltEnv) bestEffortDeleteAll(dir string) {
	fileToIsDir := make(map[string]bool)
	dEnv.FS.Iter(dir, false, func(path string, size int64, isDir bool) (stop bool) {
		fileToIsDir[path] = isDir
		return false
	})

	for path, isDir := range fileToIsDir {
		if isDir {
			dEnv.FS.Delete(path, true)
		} else {
			dEnv.FS.DeleteFile(path)
		}
	}
}

// InitRepo takes an empty directory and initializes it with a .dolt directory containing repo state, and creates a noms
// database with dolt structure.
func (dEnv *DoltEnv) InitRepo(ctx context.Context, name, email string) error { // should remove name and email args
	doltDir, err := dEnv.createDirectories(".")

	if err != nil {
		return err
	}

	err = dEnv.configureRepo(doltDir)

	if err == nil {
		err = dEnv.initDBAndState(ctx, name, email)
	}

	if err != nil {
		dEnv.bestEffortDeleteAll(doltdb.DoltDir)
	}

	return err
}

func (dEnv *DoltEnv) InitRepoWithNoData(ctx context.Context) error {
	doltDir, err := dEnv.createDirectories(".")

	if err != nil {
		return err
	}

	err = dEnv.configureRepo(doltDir)

	if err != nil {
		dEnv.bestEffortDeleteAll(doltdb.DoltDir)
	}

	dEnv.DoltDB = doltdb.LoadDoltDB(ctx, dEnv.loc)

	return err
}

func (dEnv *DoltEnv) createDirectories(dir string) (string, error) {
	doltDir := filepath.Join(dir, doltdb.DoltDir)
	if dEnv.hasDoltDir(doltDir) {
		return "", ErrPreexistingDoltDir
	}

	doltDataDir := filepath.Join(doltDir, doltdb.DataDir)
	err := dEnv.FS.MkDirs(doltDataDir)

	if err != nil {
		return "", fmt.Errorf("unable to make directory %s within the working directory", doltdb.DoltDataDir)
	}

	return doltDir, nil
}

func (dEnv *DoltEnv) configureRepo(doltDir string) error {
	err := dEnv.Config.CreateLocalConfig(map[string]string{})

	if err != nil {
		return fmt.Errorf("failed creating file %s", getLocalConfigPath())
	}

	return nil
}

func (dEnv *DoltEnv) initDBAndState(ctx context.Context, name, email string) error {
	dEnv.DoltDB = doltdb.LoadDoltDB(ctx, dEnv.loc)
	err := dEnv.DoltDB.WriteEmptyRepo(ctx, name, email)

	if err != nil {
		return doltdb.ErrNomsIO
	}

	cs, _ := doltdb.NewCommitSpec("HEAD", "master")
	commit, _ := dEnv.DoltDB.Resolve(ctx, cs)

	rootHash := commit.GetRootValue().HashOf()
	dEnv.RepoState, err = CreateRepoState(dEnv.FS, "master", rootHash)

	if err != nil {
		return ErrStateUpdate
	}

	return nil
}

func (dEnv *DoltEnv) WorkingRoot(ctx context.Context) (*doltdb.RootValue, error) {
	hashStr := dEnv.RepoState.Working
	h := hash.Parse(hashStr)

	return dEnv.DoltDB.ReadRootValue(ctx, h)
}

func (dEnv *DoltEnv) UpdateWorkingRoot(ctx context.Context, newRoot *doltdb.RootValue) error {
	h, err := dEnv.DoltDB.WriteRootValue(ctx, newRoot)

	if err != nil {
		return doltdb.ErrNomsIO
	}

	dEnv.RepoState.Working = h.String()
	err = dEnv.RepoState.Save()

	if err != nil {
		return ErrStateUpdate
	}

	return nil
}

func (dEnv *DoltEnv) HeadRoot(ctx context.Context) (*doltdb.RootValue, error) {
	cs, _ := doltdb.NewCommitSpec("head", dEnv.RepoState.Head.String())
	commit, err := dEnv.DoltDB.Resolve(ctx, cs)

	if err != nil {
		return nil, err
	}

	return commit.GetRootValue(), nil
}

func (dEnv *DoltEnv) StagedRoot(ctx context.Context) (*doltdb.RootValue, error) {
	hashStr := dEnv.RepoState.Staged
	h := hash.Parse(hashStr)

	return dEnv.DoltDB.ReadRootValue(ctx, h)
}

func (dEnv *DoltEnv) UpdateStagedRoot(ctx context.Context, newRoot *doltdb.RootValue) (hash.Hash, error) {
	h, err := dEnv.DoltDB.WriteRootValue(ctx, newRoot)

	if err != nil {
		return hash.Hash{}, doltdb.ErrNomsIO
	}

	dEnv.RepoState.Staged = h.String()
	err = dEnv.RepoState.Save()

	if err != nil {
		return hash.Hash{}, ErrStateUpdate
	}

	return h, nil
}

func (dEnv *DoltEnv) PutTableToWorking(ctx context.Context, rows types.Map, sch schema.Schema, tableName string) error {
	root, err := dEnv.WorkingRoot(ctx)

	if err != nil {
		return doltdb.ErrNomsIO
	}

	vrw := dEnv.DoltDB.ValueReadWriter()
	schVal, err := encoding.MarshalAsNomsValue(ctx, vrw, sch)

	if err != nil {
		return ErrMarshallingSchema
	}

	tbl := doltdb.NewTable(ctx, vrw, schVal, rows)
	newRoot := root.PutTable(ctx, dEnv.DoltDB, tableName, tbl)

	if root.HashOf() == newRoot.HashOf() {
		return nil
	}

	return dEnv.UpdateWorkingRoot(ctx, newRoot)
}

func (dEnv *DoltEnv) IsMergeActive() bool {
	return dEnv.RepoState.Merge != nil
}

func (dEnv *DoltEnv) GetTablesWithConflicts(ctx context.Context) ([]string, error) {
	root, err := dEnv.WorkingRoot(ctx)

	if err != nil {
		return nil, err
	}

	return root.TablesInConflict(ctx), nil
}

func (dEnv *DoltEnv) IsUnchangedFromHead(ctx context.Context) (bool, error) {
	root, err := dEnv.HeadRoot(ctx)

	if err != nil {
		return false, err
	}

	headHash := root.HashOf().String()
	if dEnv.RepoState.Working == headHash && dEnv.RepoState.Staged == headHash {
		return true, nil
	}

	return false, nil
}

func (dEnv *DoltEnv) CredsDir() (string, error) {
	return getCredsDir(dEnv.hdp)
}

func (dEnv *DoltEnv) getRPCCreds() (credentials.PerRPCCredentials, error) {
	kid, err := dEnv.Config.GetString(UserCreds)

	if err == nil && kid != "" {
		dir, err := dEnv.CredsDir()

		if err != nil {
			// not sure why you wouldn't be able to get the creds dir.
			panic(err)
		}

		dCreds, err := creds.JWKCredsReadFromFile(dEnv.FS, filepath.Join(dir, kid+".jwk"))

		if err != nil {
			return nil, ErrInvalidCredsFile
		}

		return dCreds, nil
	}

	return nil, nil
}

func (dEnv *DoltEnv) GrpcConnWithCreds(hostAndPort string, insecure bool, rpcCreds credentials.PerRPCCredentials) (*grpc.ClientConn, error) {
	var dialOpts grpc.DialOption
	if insecure {
		dialOpts = grpc.WithInsecure()
	} else {
		tc := credentials.NewTLS(&tls.Config{})
		dialOpts = grpc.WithTransportCredentials(tc)
	}

	opts := []grpc.DialOption{dialOpts, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(128 * 1024 * 1024))}

	if rpcCreds != nil {
		opts = append(opts, grpc.WithPerRPCCredentials(rpcCreds))
	}

	conn, err := grpc.Dial(hostAndPort, opts...)

	return conn, err
}

func (dEnv *DoltEnv) GrpcConn(hostAndPort string, insecure bool) (*grpc.ClientConn, error) {
	rpcCreds, err := dEnv.getRPCCreds()

	if err != nil {
		return nil, err
	}

	return dEnv.GrpcConnWithCreds(hostAndPort, insecure, rpcCreds)

}

func (dEnv *DoltEnv) GetRemotes() (map[string]Remote, error) {
	if dEnv.RSLoadErr != nil {
		return nil, dEnv.RSLoadErr
	}

	if dEnv.RepoState.Remotes == nil {
		return map[string]Remote{}, nil
	}

	return dEnv.RepoState.Remotes, nil
}

var ErrNotACred = errors.New("not a valid credential key id or public key")

func (dEnv *DoltEnv) FindCreds(credsDir, pubKeyOrId string) (string, error) {
	if !creds.B32CredsByteSet.ContainsAll([]byte(pubKeyOrId)) {
		return "", creds.ErrBadB32CredsEncoding
	}

	if len(pubKeyOrId) == creds.B32EncodedPubKeyLen {
		pubKeyOrId, _ = creds.PubKeyStrToKIDStr(pubKeyOrId)
	}

	if len(pubKeyOrId) != creds.B32EncodedKeyIdLen {
		return "", ErrNotACred
	}

	path := filepath.Join(credsDir, pubKeyOrId+creds.JWKFileExtension)
	exists, isDir := dEnv.FS.Exists(path)

	if isDir {
		return path, filesys.ErrIsDir
	} else if !exists {
		return "", creds.ErrCredsNotFound
	} else {
		return path, nil
	}
}

func (dEnv *DoltEnv) FindRef(ctx context.Context, refStr string) (ref.DoltRef, error) {
	localRef := ref.NewBranchRef(refStr)
	if dEnv.DoltDB.HasRef(ctx, localRef) {
		return localRef, nil
	} else {
		slashIdx := strings.IndexRune(refStr, '/')
		if slashIdx > 0 {
			remoteName := refStr[:slashIdx]
			if _, ok := dEnv.RepoState.Remotes[remoteName]; ok {
				remoteRef := ref.NewRemoteRefFromPathStr(refStr)

				if dEnv.DoltDB.HasRef(ctx, remoteRef) {
					return remoteRef, nil
				}
			}
		}
	}

	return ref.InvalidRef, doltdb.ErrBranchNotFound
}
