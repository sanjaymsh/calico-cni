// Copyright 2015 The etcd Authors
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

package rafthttp

import (
	"github.com/coreos/etcd/pkg/types"
	"github.com/coreos/etcd/raft/raftpb"
)

type remote struct {
	id       types.ID
	status   *peerStatus
	pipeline *pipeline
}

func startRemote(tr *Transport, urls types.URLs, to types.ID, r Raft, errorc chan error) *remote {
	picker := newURLPicker(urls)
	status := newPeerStatus(to)
	pipeline := &pipeline{
		to:     to,
		tr:     tr,
		picker: picker,
		status: status,
		raft:   r,
		errorc: errorc,
	}
	pipeline.start()

	return &remote{
		id:       to,
		status:   status,
		pipeline: pipeline,
	}
}

func (g *remote) send(m raftpb.Message) {
	select {
	case g.pipeline.msgc <- m:
	default:
		if g.status.isActive() {
			plog.MergeWarningf("dropped internal raft message to %s since sending buffer is full (bad/overloaded network)", g.id)
		}
		plog.Debugf("dropped %s to %s since sending buffer is full", m.Type, g.id)
	}
}

func (g *remote) stop() {
	g.pipeline.stop()
}
