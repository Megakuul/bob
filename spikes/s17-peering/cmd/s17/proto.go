package main

// Wire types for the spike. The SPEC proto draft is in docs/spikes/s17-peering.md; here the
// same messages are plain Go structs sent with connectrpc and a JSON codec, so no codegen is
// needed for a throwaway spike.

import (
	"encoding/json"
	"hash/fnv"
	"sort"
)

const (
	procPush       = "/bob.worker.v1.LiveLogService/PushLines"     // worker -> orchestrator (unary)
	procPeerFollow = "/bob.peer.v1.PeerLogService/Follow"          // orchestrator -> orchestrator (server stream)
	procFollow     = "/bob.log.v1.LogService/FollowLog"            // client -> orchestrator (server stream)
)

type Line struct {
	Pkg    string `json:"p"`
	Seq    int64  `json:"s"`
	Att    int32  `json:"a"`
	Stream int32  `json:"st,omitempty"`
	TS     int64  `json:"t"` // unix ns, read time on the worker
	Text   string `json:"x"`
}

// Restart says: attempt Att of package Pkg continues at seq From; lines with seq >= From of
// older attempts were never persisted and are void.
type Restart struct {
	Pkg  string `json:"p"`
	Att  int32  `json:"a"`
	From int64  `json:"f"`
}

// Final is the end marker of one package: the last seq of the valid log.
type Final struct {
	Pkg string `json:"p"`
	Seq int64  `json:"s"`
	Att int32  `json:"a"`
}

type PushReq struct {
	Build    string    `json:"b"`
	Lines    []Line    `json:"l,omitempty"`
	Restarts []Restart `json:"r,omitempty"`
	End      []Final   `json:"e,omitempty"` // set once, after the last lake commit
}
type PushRes struct {
	Accepted int `json:"n"`
}

type Cursor struct {
	Seq int64 `json:"s"`
	Att int32 `json:"a"`
}

type FollowReq struct {
	Build  string            `json:"b"`
	Cursor map[string]Cursor `json:"c,omitempty"` // package -> last delivered (seq, attempt)
}

type FollowMsg struct {
	Lines    []Line    `json:"l,omitempty"`
	Restarts []Restart `json:"r,omitempty"`
	End      []Final   `json:"e,omitempty"`
	Done     bool      `json:"d,omitempty"`
	Degraded bool      `json:"g,omitempty"` // client side: live source missing, lake only
}

type jsonCodec struct{}

func (jsonCodec) Name() string                     { return "json" }
func (jsonCodec) Marshal(v any) ([]byte, error)    { return json.Marshal(v) }
func (jsonCodec) Unmarshal(b []byte, v any) error  { return json.Unmarshal(b, v) }

func size(v any) int64 { b, _ := json.Marshal(v); return int64(len(b)) }

// rendezvous returns orchestrator ids ordered by highest-random-weight for the build.
func rendezvous(build string, ids []string) []string {
	out := append([]string(nil), ids...)
	score := func(id string) uint64 { h := fnv.New64a(); h.Write([]byte(build + "|" + id)); return h.Sum64() }
	sort.Slice(out, func(i, j int) bool { return score(out[i]) > score(out[j]) })
	return out
}
