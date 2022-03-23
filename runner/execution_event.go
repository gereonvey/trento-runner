package runner

import (
	"github.com/google/uuid"
)

type ExecutionEvent struct {
	ID       uuid.UUID  `json:"execution_id" binding:"required"`
	Clusters []*Cluster `json:"clusters" binding:"required"`
}

type Cluster struct {
	ID       uuid.UUID `json:"cluster_id" binding:"required"`
	Provider string    `json:"provider" binding:"required"`
	Checks   []string  `json:"checks" binding:"required"`
	Hosts    []*Host   `json:"hosts" binding:"required"`
}

type Host struct {
	ID      uuid.UUID `json:"host_id" binding:"required"`
	Address string    `json:"address" binding:"required"`
	User    string    `json:"user" binding:"required"`
}
