package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/nodeupdate"
	"mikan/internal/release"
)

func (h *handlers) registerNodeUpdates() {
	huma.Register(h.api, huma.Operation{OperationID: "update-node-version", Method: http.MethodPost, Path: "/api/v1/nodes/{id}/update", Summary: "Обновить ноду до версии панели", Tags: []string{"node"}, DefaultStatus: http.StatusAccepted}, h.updateNodeVersion)
	huma.Register(h.api, huma.Operation{OperationID: "update-nodes", Method: http.MethodPost, Path: "/api/v1/nodes/update-all", Summary: "Обновить отстающие ноды до версии панели, по одной", Tags: []string{"node"}, DefaultStatus: http.StatusAccepted}, h.updateAllNodes)
}

// nodeUpdateStatus is what the Nodes page shows of a node's update: the updater's own
// report when the node sent one, else what the panel knows of its request.
func nodeUpdateStatus(a nodeupdate.Attempt, asked bool, reported *nodeapi.UpdateStatus, nodeVersion string) *nodeapi.UpdateStatus {
	own := func(state string) *nodeapi.UpdateStatus {
		return &nodeapi.UpdateStatus{State: state, Version: a.Version, From: a.From, Error: a.Error, At: time.Unix(a.At, 0).UTC().Format(time.RFC3339)}
	}
	switch {
	case asked && a.State == nodeupdate.Running:
		if reported != nil && reported.State != nodeapi.UpdateOK {
			return reported
		}
		return own(nodeupdate.Running)
	case asked && a.State == nodeupdate.Failed && !release.AtLeast(nodeVersion, a.Version):
		if reported != nil && reported.State == nodeapi.UpdateFailed {
			return reported
		}
		return own(nodeupdate.Failed)
	}
	return reported
}

// nodeUpdateView fills the update fields of a node's view: how it compares with the panel,
// whether it can be updated from here, and how its update goes.
func (h *handlers) nodeUpdateView(ctx context.Context, v *NodeInfo, reported *nodeapi.UpdateStatus) {
	v.Update = reported
	if v.Local || v.Status != "ok" {
		return
	}
	v.Behind = nodeupdate.Behind(h.d.Version, v.Version)
	v.CanUpdate = nodeupdate.CanUpdate(v.Version)
	if h.d.NodeUpdates != nil {
		a, asked := h.d.NodeUpdates.Attempt(ctx, v.ID)
		v.Update = nodeUpdateStatus(a, asked, reported, v.Version)
	}
}

func (h *handlers) updateNodeVersion(ctx context.Context, in *nodeIDInput) (*nodeInfoOutput, error) {
	n, err := h.getNode(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if h.d.NodeUpdates == nil {
		return nil, huma.Error409Conflict("nodes_disabled")
	}
	if err := h.d.NodeUpdates.Request(ctx, n); err != nil {
		return nil, nodeUpdateError(err, h.d.Log, n.ID)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "node.update_version", "node", strconv.FormatInt(n.ID, 10), map[string]any{"name": n.Name, "version": h.d.Version})
	return h.nodeInfo(ctx, n.ID)
}

func (h *handlers) updateAllNodes(ctx context.Context, _ *struct{}) (*nodesOutput, error) {
	if h.d.NodeUpdates == nil {
		return nil, huma.Error409Conflict("nodes_disabled")
	}
	if err := h.d.NodeUpdates.RequestAll(ctx); err != nil {
		return nil, nodeUpdateError(err, h.d.Log, 0)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "node.update_all", "", "", map[string]any{"version": h.d.Version})
	return h.listNodes(ctx, nil)
}

// nodeUpdateError turns the service's refusals into the API's codes; whatever else went
// wrong is the node's or the network's.
func nodeUpdateError(err error, log *slog.Logger, node int64) error {
	switch {
	case errors.Is(err, nodeupdate.ErrLocal):
		return huma.Error409Conflict("local_node_update")
	case errors.Is(err, nodeupdate.ErrNotRelease):
		return huma.Error409Conflict("panel_not_release")
	case errors.Is(err, nodeupdate.ErrOffline):
		return huma.Error409Conflict("node_offline")
	case errors.Is(err, nodeupdate.ErrNotBehind):
		return huma.Error409Conflict("node_not_behind")
	case errors.Is(err, nodeupdate.ErrUnsupported):
		return huma.Error409Conflict("node_cannot_update")
	case errors.Is(err, nodeupdate.ErrBusy):
		return huma.Error409Conflict("update_running")
	}
	if log != nil {
		log.Warn("node update request", "node", node, "err", err)
	}
	if errors.Is(err, nodeapi.ErrUnavailable) {
		return huma.Error502BadGateway("node_unavailable")
	}
	return huma.Error502BadGateway("node_update_failed")
}
