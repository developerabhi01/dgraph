/*
 * Copyright 2020 Dgraph Labs, Inc. and Contributors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package worker

import (
	"fmt"
	"sync"

	"github.com/dgraph-io/dgraph/protos/pb"
	"github.com/pkg/errors"
)

// predicateSet is a map whose keys are predicates. It is meant to be used as a set.
type predicateSet map[string]struct{}

// Manifest records backup details, these are values used during restore.
// Since is the timestamp from which the next incremental backup should start (it's set
// to the readTs of the current backup).
// Groups are the IDs of the groups involved.
type Manifest struct {
	sync.Mutex
	//Type is the type of backup, either full or incremental.
	Type string `json:"type"`
	// Since is the timestamp at which this backup was taken. It's called Since
	// because it will become the timestamp from which to backup in the next
	// incremental backup.
	Since uint64 `json:"since"`
	// Groups is the map of valid groups to predicates at the time the backup was created.
	Groups map[uint32][]string `json:"groups"`
	// BackupId is a unique ID assigned to all the backups in the same series
	// (from the first full backup to the last incremental backup).
	BackupId string `json:"backup_id"`
	// BackupNum is a monotonically increasing number assigned to each backup in
	// a series. The full backup as BackupNum equal to one and each incremental
	// backup gets assigned the next available number. Used to verify the integrity
	// of the data during a restore.
	BackupNum uint64 `json:"backup_num"`
	// Path is the path to the manifest file. This field is only used during
	// processing and is not written to disk.
	Path string `json:"-"`
	// Encrypted indicates whether this backup was encrypted or not.
	Encrypted bool `json:"encrypted"`
}

func (m *Manifest) getPredsInGroup(gid uint32) predicateSet {
	preds, ok := m.Groups[gid]
	if !ok {
		return nil
	}

	predSet := make(predicateSet)
	for _, pred := range preds {
		predSet[pred] = struct{}{}
	}
	return predSet
}

// Credentials holds the credentials needed to perform a backup operation.
// If these credentials are missing the default credentials will be used.
type Credentials struct {
	AccessKey    string
	SecretKey    string
	SessionToken string
	Anonymous    bool
}

func (creds *Credentials) isAnonymous() bool {
	if creds == nil {
		return false
	}
	return creds.Anonymous
}

// GetCredentialsFromRequest extracts the credentials from a backup request.
func GetCredentialsFromRequest(req *pb.BackupRequest) *Credentials {
	return &Credentials{
		AccessKey:    req.GetAccessKey(),
		SecretKey:    req.GetSecretKey(),
		SessionToken: req.GetSessionToken(),
		Anonymous:    req.GetAnonymous(),
	}
}

type RestoreStatus struct {
	// status is a string representing the status, one of "UNKNOWN", "IN_PROGRESS", "OK",
	// or "ERR".
	status string
	errors []error
}

type restoreTracker struct {
	sync.RWMutex
	// status is a map of restore task ID to the status of said task.
	status  map[string]*RestoreStatus
	counter int
}

func newRestoreTracker() *restoreTracker {
	return &restoreTracker{status: make(map[string]*RestoreStatus)}
}

func (rt *restoreTracker) Status(restoreId string) *RestoreStatus {
	if rt == nil {
		return &RestoreStatus{status: "UNKNOWN"}
	}

	rt.RLock()
	defer rt.RUnlock()

	status, ok := rt.status[restoreId]
	if ok {
		return status
	}
	return &RestoreStatus{status: "UNKNOWN"}
}

func (rt *restoreTracker) Add() (string, error) {
	if rt == nil {
		return "", errors.Errorf("uninitialized restore operation tracker")
	}

	rt.Lock()
	defer rt.Unlock()

	rt.counter += 1
	restoreId := fmt.Sprintf("restore-%d", rt.counter)
	if _, ok := rt.status[restoreId]; ok {
		return "", errors.Errorf("another restore operation with ID %s already exists", restoreId)
	}

	rt.status[restoreId] = &RestoreStatus{status: "IN_PROGRESS", errors: make([]error, 0)}
	return restoreId, nil
}

func (rt *restoreTracker) Done(restoreId string, errs []error) error {
	if rt == nil {
		return errors.Errorf("uninitialized restore operation tracker")
	}

	if restoreId == "" {
		return errors.Errorf("restoreId cannot be empty")
	}

	rt.Lock()
	defer rt.Unlock()

	if _, ok := rt.status[restoreId]; !ok {
		return errors.Errorf("unknown restore operation with ID %s", restoreId)
	}

	status := "OK"
	validErrs := make([]error, 0)
	for _, err := range errs {
		if err == nil {
			continue
		}
		validErrs = append(validErrs, err)
		status = "ERR"
	}
	rt.status[restoreId] = &RestoreStatus{status: status, errors: validErrs}
	return nil
}
