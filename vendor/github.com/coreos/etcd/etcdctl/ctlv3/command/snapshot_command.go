// Copyright 2016 CoreOS, Inc.
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

package command

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"strings"

	"github.com/boltdb/bolt"
	"github.com/coreos/etcd/etcdserver"
	"github.com/coreos/etcd/etcdserver/etcdserverpb"
	"github.com/coreos/etcd/etcdserver/membership"
	"github.com/coreos/etcd/pkg/fileutil"
	"github.com/coreos/etcd/pkg/types"
	"github.com/coreos/etcd/raft"
	"github.com/coreos/etcd/raft/raftpb"
	"github.com/coreos/etcd/storage"
	"github.com/coreos/etcd/storage/backend"
	"github.com/coreos/etcd/wal"
	"github.com/spf13/cobra"
	"golang.org/x/net/context"
)

const (
	defaultName                     = "default"
	defaultInitialAdvertisePeerURLs = "http://localhost:2380,http://localhost:7001"
)

var (
	restoreCluster      string
	restoreClusterToken string
	restoreDataDir      string
	restorePeerURLs     string
	restoreName         string
)

// NewSnapshotCommand returns the cobra command for "snapshot".
func NewSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "snapshot manages etcd node snapshots.",
	}
	cmd.AddCommand(NewSnapshotSaveCommand())
	cmd.AddCommand(NewSnapshotRestoreCommand())
	cmd.AddCommand(newSnapshotStatusCommand())
	return cmd
}

func NewSnapshotSaveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "save <filename>",
		Short: "save stores an etcd node backend snapshot to a given file.",
		Run:   snapshotSaveCommandFunc,
	}
}

func newSnapshotStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status <filename>",
		Short: "status gets backend snapshot status of a given file.",
		Run:   snapshotStatusCommandFunc,
	}
}

func NewSnapshotRestoreCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <filename>",
		Short: "restore an etcd member snapshot to an etcd directory",
		Run:   snapshotRestoreCommandFunc,
	}
	cmd.Flags().StringVar(&restoreDataDir, "data-dir", "", "Path to the data directory.")
	cmd.Flags().StringVar(&restoreCluster, "initial-cluster", initialClusterFromName(defaultName), "Initial cluster configuration for restore bootstrap.")
	cmd.Flags().StringVar(&restoreClusterToken, "initial-cluster-token", "etcd-cluster", "Initial cluster token for the etcd cluster during restore bootstrap.")
	cmd.Flags().StringVar(&restorePeerURLs, "initial-advertise-peer-urls", defaultInitialAdvertisePeerURLs, "List of this member's peer URLs to advertise to the rest of the cluster.")
	cmd.Flags().StringVar(&restoreName, "name", defaultName, "Human-readable name for this member.")

	return cmd
}

func snapshotSaveCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) != 1 {
		err := fmt.Errorf("snapshot save expects one argument")
		ExitWithError(ExitBadArgs, err)
	}

	path := args[0]

	partpath := path + ".part"
	f, err := os.Create(partpath)
	defer f.Close()
	if err != nil {
		exiterr := fmt.Errorf("could not open %s (%v)", partpath, err)
		ExitWithError(ExitBadArgs, exiterr)
	}

	c := mustClientFromCmd(cmd)
	r, serr := c.Snapshot(context.TODO())
	if serr != nil {
		os.RemoveAll(partpath)
		ExitWithError(ExitInterrupted, serr)
	}
	if _, rerr := io.Copy(f, r); rerr != nil {
		os.RemoveAll(partpath)
		ExitWithError(ExitInterrupted, rerr)
	}

	fileutil.Fsync(f)

	if rerr := os.Rename(partpath, path); rerr != nil {
		exiterr := fmt.Errorf("could not rename %s to %s (%v)", partpath, path, rerr)
		ExitWithError(ExitIO, exiterr)
	}
	fmt.Printf("Snapshot saved at %s\n", path)
}

func snapshotStatusCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) != 1 {
		err := fmt.Errorf("snapshot status requires exactly one argument")
		ExitWithError(ExitBadArgs, err)
	}
	initDisplayFromCmd(cmd)
	ds := dbStatus(args[0])
	display.DBStatus(ds)
}

func snapshotRestoreCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) != 1 {
		err := fmt.Errorf("snapshot restore requires exactly one argument")
		ExitWithError(ExitBadArgs, err)
	}

	urlmap, uerr := types.NewURLsMap(restoreCluster)
	if uerr != nil {
		ExitWithError(ExitBadArgs, uerr)
	}

	cfg := etcdserver.ServerConfig{
		InitialClusterToken: restoreClusterToken,
		InitialPeerURLsMap:  urlmap,
		PeerURLs:            types.MustNewURLs(strings.Split(restorePeerURLs, ",")),
		Name:                restoreName,
	}
	if err := cfg.VerifyBootstrap(); err != nil {
		ExitWithError(ExitBadArgs, err)
	}

	cl, cerr := membership.NewClusterFromURLsMap(restoreClusterToken, urlmap)
	if cerr != nil {
		ExitWithError(ExitBadArgs, cerr)
	}

	basedir := restoreDataDir
	if basedir == "" {
		basedir = restoreName + ".etcd"
	}

	waldir := path.Join(basedir, "member", "wal")
	snapdir := path.Join(basedir, "member", "snap")

	if _, err := os.Stat(basedir); err == nil {
		ExitWithError(ExitInvalidInput, fmt.Errorf("data-dir %q exists", basedir))
	}

	makeDB(snapdir, args[0])
	makeWAL(waldir, cl)
}

func initialClusterFromName(name string) string {
	n := name
	if name == "" {
		n = defaultName
	}
	return fmt.Sprintf("%s=http://localhost:2380,%s=http://localhost:7001", n, n)
}

// makeWAL creates a WAL for the initial cluster
func makeWAL(waldir string, cl *membership.RaftCluster) {
	if err := os.MkdirAll(waldir, 0755); err != nil {
		ExitWithError(ExitIO, err)
	}

	m := cl.MemberByName(restoreName)
	md := &etcdserverpb.Metadata{NodeID: uint64(m.ID), ClusterID: uint64(cl.ID())}
	metadata, merr := md.Marshal()
	if merr != nil {
		ExitWithError(ExitInvalidInput, merr)
	}

	w, walerr := wal.Create(waldir, metadata)
	if walerr != nil {
		ExitWithError(ExitIO, walerr)
	}
	defer w.Close()

	peers := make([]raft.Peer, len(cl.MemberIDs()))
	for i, id := range cl.MemberIDs() {
		ctx, err := json.Marshal((*cl).Member(id))
		if err != nil {
			ExitWithError(ExitInvalidInput, err)
		}
		peers[i] = raft.Peer{ID: uint64(id), Context: ctx}
	}

	ents := make([]raftpb.Entry, len(peers))
	for i, p := range peers {
		cc := raftpb.ConfChange{
			Type:    raftpb.ConfChangeAddNode,
			NodeID:  p.ID,
			Context: p.Context}
		d, err := cc.Marshal()
		if err != nil {
			ExitWithError(ExitInvalidInput, err)
		}
		e := raftpb.Entry{
			Type:  raftpb.EntryConfChange,
			Term:  1,
			Index: uint64(i + 1),
			Data:  d,
		}
		ents[i] = e
	}

	w.Save(raftpb.HardState{
		Term:   1,
		Vote:   peers[0].ID,
		Commit: uint64(len(ents))}, ents)
}

// initIndex implements ConsistentIndexGetter so the snapshot won't block
// the new raft instance by waiting for a future raft index.
type initIndex struct{}

func (*initIndex) ConsistentIndex() uint64 { return 1 }

// makeDB copies the database snapshot to the snapshot directory
func makeDB(snapdir, dbfile string) {
	f, ferr := os.OpenFile(dbfile, os.O_RDONLY, 0600)
	if ferr != nil {
		ExitWithError(ExitInvalidInput, ferr)
	}
	defer f.Close()

	if err := os.MkdirAll(snapdir, 0755); err != nil {
		ExitWithError(ExitIO, err)
	}

	dbpath := path.Join(snapdir, "db")
	db, dberr := os.OpenFile(dbpath, os.O_WRONLY|os.O_CREATE, 0600)
	if dberr != nil {
		ExitWithError(ExitIO, dberr)
	}
	if _, err := io.Copy(db, f); err != nil {
		ExitWithError(ExitIO, err)
	}
	db.Close()

	// update consistentIndex so applies go through on etcdserver despite
	// having a new raft instance
	be := backend.NewDefaultBackend(dbpath)
	s := storage.NewStore(be, nil, &initIndex{})
	id := s.TxnBegin()
	btx := be.BatchTx()
	del := func(k, v []byte) error {
		_, _, err := s.TxnDeleteRange(id, k, nil)
		return err
	}
	// delete stored members from old cluster since using new members
	btx.UnsafeForEach([]byte("members"), del)
	btx.UnsafeForEach([]byte("members_removed"), del)
	// trigger write-out of new consistent index
	s.TxnEnd(id)
	s.Commit()
	s.Close()
}

type dbstatus struct {
	Hash      uint32 `json:"hash"`
	Revision  int64  `json:"revision"`
	TotalKey  int    `json:"totalKey"`
	TotalSize int64  `json:"totalSize"`
}

func dbStatus(p string) dbstatus {
	if _, err := os.Stat(p); err != nil {
		ExitWithError(ExitError, err)
	}

	ds := dbstatus{}

	db, err := bolt.Open(p, 0600, nil)
	if err != nil {
		ExitWithError(ExitError, err)
	}

	h := crc32.New(crc32.MakeTable(crc32.Castagnoli))

	err = db.View(func(tx *bolt.Tx) error {
		ds.TotalSize = tx.Size()
		c := tx.Cursor()
		for next, _ := c.First(); next != nil; next, _ = c.Next() {
			b := tx.Bucket(next)
			if b == nil {
				return fmt.Errorf("cannot get hash of bucket %s", string(next))
			}
			h.Write(next)
			iskeyb := (string(next) == "key")
			b.ForEach(func(k, v []byte) error {
				h.Write(k)
				h.Write(v)
				if iskeyb {
					rev := bytesToRev(k)
					ds.Revision = rev.main
				}
				ds.TotalKey++
				return nil
			})
		}
		return nil
	})

	if err != nil {
		ExitWithError(ExitError, err)
	}

	ds.Hash = h.Sum32()
	return ds
}

type revision struct {
	main int64
	sub  int64
}

func bytesToRev(bytes []byte) revision {
	return revision{
		main: int64(binary.BigEndian.Uint64(bytes[0:8])),
		sub:  int64(binary.BigEndian.Uint64(bytes[9:])),
	}
}
