package distributed

import "uk.ac.bris.cs/gameoflife/util"

var BrokerAddress = "127.0.0.1:8030"
var BackupBrokerAddresses = []string{
	"127.0.0.1:8040",
	"127.0.0.1:8041",
}
var WorkerAddresses = []string{
	"127.0.0.1:8031",
	"127.0.0.1:8032",
	"127.0.0.1:8033",
	"127.0.0.1:8034",
}

type InitRequest struct {
	World       [][]byte
	ImageWidth  int
	ImageHeight int
	StartY      int
	EndY        int
	WorkerID    int
	Threads     int
}

type InitResponse struct {
	Success bool
}

type HaloRequest struct {
	Row     []byte
	IsTop   bool
	TurnNum int
}

type HaloResponse struct {
	Success bool
}

type GetHaloRowRequest struct {
	IsTop   bool
	TurnNum int
}

type GetHaloRowResponse struct {
	Row []byte
}

type ProcessTurnRequest struct {
	TurnNum int
}

type ProcessTurnResponse struct {
	Flipped []util.Cell
}

type GetStateRequest struct{}

type GetStateResponse struct {
	Section [][]byte
}

type BrokerInitRequest struct {
	World       [][]byte
	ImageWidth  int
	ImageHeight int
	Threads     int
}

type BrokerInitResponse struct {
	Success bool
}

type BrokerTurnRequest struct {
	TurnNum int
}

type BrokerTurnResponse struct {
	Flipped []util.Cell
}

type BrokerStateRequest struct{}

type BrokerStateResponse struct {
	World [][]byte
	Turn  int
}

type BrokerAliveRequest struct{}

type BrokerAliveResponse struct {
	Count int
	Cells []util.Cell
}

type BrokerShutdownRequest struct{}

type BrokerShutdownResponse struct {
	Success bool
}

type BrokerHealthRequest struct{}

type BrokerHealthResponse struct {
	Healthy bool
	Turn    int
}

type BrokerSyncRequest struct {
	World [][]byte
	Turn  int
}

type BrokerSyncResponse struct {
	Success bool
}

type BrokerHeartbeatRequest struct {
	BrokerID string
	Term     int
	IsLeader bool
	LastTurn int
}

type BrokerHeartbeatResponse struct {
	Success  bool
	Term     int
	IsLeader bool
}

type Worker interface {
	Init(req InitRequest, res *InitResponse) error
	ProcessTurn(req ProcessTurnRequest, res *ProcessTurnResponse) error
	ReceiveHalo(req HaloRequest, res *HaloResponse) error
	GetHaloRow(req GetHaloRowRequest, res *GetHaloRowResponse) error
	GetState(req GetStateRequest, res *GetStateResponse) error
	Shutdown(req struct{}, res *struct{}) error
}

type Broker interface {
	Init(req BrokerInitRequest, res *BrokerInitResponse) error
	ProcessTurn(req BrokerTurnRequest, res *BrokerTurnResponse) error
	GetState(req BrokerStateRequest, res *BrokerStateResponse) error
	GetAlive(req BrokerAliveRequest, res *BrokerAliveResponse) error
	Shutdown(req BrokerShutdownRequest, res *BrokerShutdownResponse) error
	Health(req BrokerHealthRequest, res *BrokerHealthResponse) error
	Sync(req BrokerSyncRequest, res *BrokerSyncResponse) error
	Heartbeat(req BrokerHeartbeatRequest, res *BrokerHeartbeatResponse) error
}
