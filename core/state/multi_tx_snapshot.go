package state

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"

	"github.com/ethereum/go-ethereum/common"
)

// MultiTxSnapshot retains StateDB changes for multiple transactions.
type MultiTxSnapshot struct {
	invalid bool

	numLogsAdded map[common.Hash]int

	prevObjects map[common.Address]*stateObject

	accountStorage  map[common.Address]map[common.Hash]*common.Hash
	accountBalance  map[common.Address]*big.Int
	accountNonce    map[common.Address]uint64
	accountCode     map[common.Address][]byte
	accountCodeHash map[common.Address][]byte

	accountSuicided map[common.Address]bool
	accountDeleted  map[common.Address]bool

	accountNotPending map[common.Address]struct{}
	accountNotDirty   map[common.Address]struct{}
	// TODO: snapdestructs, snapaccount storage
}

// NewMultiTxSnapshot creates a new MultiTxSnapshot
func NewMultiTxSnapshot() *MultiTxSnapshot {
	multiTxSnapshot := newMultiTxSnapshot()
	return &multiTxSnapshot
}

func newMultiTxSnapshot() MultiTxSnapshot {
	return MultiTxSnapshot{
		numLogsAdded:      make(map[common.Hash]int),
		prevObjects:       make(map[common.Address]*stateObject),
		accountStorage:    make(map[common.Address]map[common.Hash]*common.Hash),
		accountBalance:    make(map[common.Address]*big.Int),
		accountNonce:      make(map[common.Address]uint64),
		accountCode:       make(map[common.Address][]byte),
		accountCodeHash:   make(map[common.Address][]byte),
		accountSuicided:   make(map[common.Address]bool),
		accountDeleted:    make(map[common.Address]bool),
		accountNotPending: make(map[common.Address]struct{}),
		accountNotDirty:   make(map[common.Address]struct{}),
	}
}

// updateFromJournal updates the snapshot with the changes from the journal.
func (s *MultiTxSnapshot) updateFromJournal(journal *journal) {
	for _, journalEntry := range journal.entries {
		switch entry := journalEntry.(type) {
		case balanceChange:
			s.updateBalanceChange(entry)
		case nonceChange:
			s.updateNonceChange(entry)
		case codeChange:
			s.updateCodeChange(entry)
		case addLogChange:
			s.numLogsAdded[entry.txhash]++
		case createObjectChange:
			s.updateCreateObjectChange(entry)
		case resetObjectChange:
			s.updateResetObjectChange(entry)
		case suicideChange:
			s.updateSuicideChange(entry)
		}
	}
}

// objectChanged returns whether the object was changed (in the set of prevObjects).
func (s *MultiTxSnapshot) objectChanged(address common.Address) bool {
	_, ok := s.prevObjects[address]
	return ok
}

// updateBalanceChange updates the snapshot with the balance change.
func (s *MultiTxSnapshot) updateBalanceChange(change balanceChange) {
	if s.objectChanged(*change.account) {
		return
	}
	if _, ok := s.accountBalance[*change.account]; !ok {
		s.accountBalance[*change.account] = change.prev
	}
}

// updateNonceChange updates the snapshot with the nonce change.
func (s *MultiTxSnapshot) updateNonceChange(change nonceChange) {
	if s.objectChanged(*change.account) {
		return
	}
	if _, ok := s.accountNonce[*change.account]; !ok {
		s.accountNonce[*change.account] = change.prev
	}
}

// updateCodeChange updates the snapshot with the code change.
func (s *MultiTxSnapshot) updateCodeChange(change codeChange) {
	if s.objectChanged(*change.account) {
		return
	}
	if _, ok := s.accountCode[*change.account]; !ok {
		s.accountCode[*change.account] = change.prevcode
		s.accountCodeHash[*change.account] = change.prevhash
	}
}

// updateResetObjectChange updates the snapshot with the reset object change.
func (s *MultiTxSnapshot) updateResetObjectChange(change resetObjectChange) {
	address := change.prev.address
	if _, ok := s.prevObjects[address]; !ok {
		s.prevObjects[address] = change.prev
	}
}

// updateCreateObjectChange updates the snapshot with the createObjectChange.
func (s *MultiTxSnapshot) updateCreateObjectChange(change createObjectChange) {
	if _, ok := s.prevObjects[*change.account]; !ok {
		s.prevObjects[*change.account] = nil
	}
}

// updateSuicideChange updates the snapshot with the suicide change.
func (s *MultiTxSnapshot) updateSuicideChange(change suicideChange) {
	if s.objectChanged(*change.account) {
		return
	}
	if _, ok := s.accountSuicided[*change.account]; !ok {
		s.accountSuicided[*change.account] = change.prev
	}
	if _, ok := s.accountBalance[*change.account]; !ok {
		s.accountBalance[*change.account] = change.prevbalance
	}
}

// updatePendingStorage updates the snapshot with the pending storage change.
func (s *MultiTxSnapshot) updatePendingStorage(address common.Address, key, value common.Hash, ok bool) {
	if s.objectChanged(address) {
		return
	}
	if _, exists := s.accountStorage[address]; !exists {
		s.accountStorage[address] = make(map[common.Hash]*common.Hash)
	}
	if _, exists := s.accountStorage[address][key]; exists {
		return
	}
	if ok {
		s.accountStorage[address][key] = &value
	} else {
		s.accountStorage[address][key] = nil
	}
}

// updatePendingStatus updates the snapshot with previous pending status.
func (s *MultiTxSnapshot) updatePendingStatus(address common.Address, pending, dirty bool) {
	if !pending {
		s.accountNotPending[address] = struct{}{}
	}
	if !dirty {
		s.accountNotDirty[address] = struct{}{}
	}
}

// updateObjectDeleted updates the snapshot with the object deletion.
func (s *MultiTxSnapshot) updateObjectDeleted(address common.Address, deleted bool) {
	if s.objectChanged(address) {
		return
	}
	if _, ok := s.accountDeleted[address]; !ok {
		s.accountDeleted[address] = deleted
	}
}

// Merge merges the changes from another snapshot into the current snapshot.
// The operation assumes that the other snapshot is later (newer) than the current snapshot.
// Changes are merged such that older state is retained and not overwritten.
// In other words, this method performs a union operation on two snapshots, where
// older values are retained and any new values are added to the current snapshot.
func (s *MultiTxSnapshot) Merge(other *MultiTxSnapshot) error {
	if other.invalid || s.invalid {
		return errors.New("failed to merge snapshots - invalid snapshot found")
	}

	// each snapshot increments the number of logs per transaction hash
	// when we merge snapshots, the number of logs added per transaction are appended to current snapshot
	for txHash, numLogs := range other.numLogsAdded {
		s.numLogsAdded[txHash] += numLogs
	}

	// prevObjects contain mapping of address to state objects
	// if the current snapshot has previous object for same address, retain previous object
	// otherwise, add new object from other snapshot
	for address, object := range other.prevObjects {
		if _, exist := s.prevObjects[address]; !exist {
			s.prevObjects[address] = object
		}
	}

	// merge account storage -
	//   we want to retain any existing storage values for a given account,
	//   update storage keys if they do not exist for a given account's storage,
	//   and update pending storage for accounts that don't already exist in current snapshot
	for address, storage := range other.accountStorage {
		for key, value := range storage {
			if value == nil {
				s.updatePendingStorage(address, key, types.EmptyCodeHash, false)
			} else {
				s.updatePendingStorage(address, key, common.BytesToHash(value.Bytes()), true)
			}
		}
	}

	// add previous balance(s) for any addresses that don't exist in current snapshot
	for address, balance := range other.accountBalance {
		if _, exist := s.accountBalance[address]; !exist {
			s.accountBalance[address] = balance
		}
	}

	// add previous nonce for accounts that don't exist in current snapshot
	for address, nonce := range other.accountNonce {
		if _, exist := s.accountNonce[address]; !exist {
			s.accountNonce[address] = nonce
		}
	}

	// add previous code for accounts not found in current snapshot
	for address, code := range other.accountCode {
		if _, exist := s.accountCode[address]; !exist {
			if _, found := other.accountCodeHash[address]; !found {
				// every codeChange has code and code hash set -
				//   should never reach this point unless there is programming error
				panic("snapshot merge found code but no code hash for account address")
			}

			s.accountCode[address] = code
			s.accountCodeHash[address] = other.accountCodeHash[address]
		}
	}

	// add previous suicide for addresses not in current snapshot
	for address, suicided := range other.accountSuicided {
		if _, exist := s.accountSuicided[address]; !exist {
			s.accountSuicided[address] = suicided
		} else {
			return errors.New("failed to merge snapshots - duplicate found for account suicide")
		}
	}

	// add previous account deletions if they don't exist
	for address, deleted := range other.accountDeleted {
		if _, exist := s.accountDeleted[address]; !exist {
			s.accountDeleted[address] = deleted
		}
	}

	// add previous pending status if not found
	for address := range other.accountNotPending {
		if _, exist := s.accountNotPending[address]; !exist {
			s.accountNotPending[address] = struct{}{}
		}
	}

	for address := range other.accountNotDirty {
		if _, exist := s.accountNotDirty[address]; !exist {
			s.accountNotDirty[address] = struct{}{}
		}
	}

	return nil
}

// revertState reverts the state to the snapshot.
func (s *MultiTxSnapshot) revertState(st *StateDB) {
	// remove all the logs added
	for txhash, numLogs := range s.numLogsAdded {
		lens := len(st.logs[txhash])
		if lens == numLogs {
			delete(st.logs, txhash)
		} else {
			st.logs[txhash] = st.logs[txhash][:lens-numLogs]
		}
		st.logSize -= uint(numLogs)
	}

	// restore the objects
	for address, object := range s.prevObjects {
		if object == nil {
			delete(st.stateObjects, address)
		} else {
			st.stateObjects[address] = object
		}
	}

	// restore storage
	for address, storage := range s.accountStorage {
		for key, value := range storage {
			if value == nil {
				delete(st.stateObjects[address].pendingStorage, key)
			} else {
				st.stateObjects[address].pendingStorage[key] = *value
			}
		}
	}

	// restore balance
	for address, balance := range s.accountBalance {
		st.stateObjects[address].setBalance(balance)
	}
	// restore nonce
	for address, nonce := range s.accountNonce {
		st.stateObjects[address].setNonce(nonce)
	}
	// restore code
	for address, code := range s.accountCode {
		st.stateObjects[address].setCode(common.BytesToHash(s.accountCodeHash[address]), code)
	}
	// restore suicided
	for address, suicided := range s.accountSuicided {
		st.stateObjects[address].suicided = suicided
	}
	// restore deleted
	for address, deleted := range s.accountDeleted {
		st.stateObjects[address].deleted = deleted
	}

	// restore pending status
	for address := range s.accountNotPending {
		delete(st.stateObjectsPending, address)
	}
	for address := range s.accountNotDirty {
		delete(st.stateObjectsDirty, address)
	}
}

// MultiTxSnapshotStack contains a list of snapshots for multiple transactions associated with a StateDB.
// Intended use is as follows:
//   - Create a new snapshot and push on top of the stack
//   - Apply transactions to state and update head snapshot with changes from journal
//   - If any changes applied to state database are committed to trie, invalidate the head snapshot
//   - If applied changes are not desired, revert the changes from the head snapshot and pop the snapshot from the stack
//   - If applied changes are desired, commit the changes from the head snapshot by merging with previous entry
//     and pop the snapshot from the stack
type MultiTxSnapshotStack struct {
	snapshots []MultiTxSnapshot
	state     *StateDB
}

// NewMultiTxSnapshotStack creates a new MultiTxSnapshotStack with a given StateDB.
func NewMultiTxSnapshotStack(state *StateDB) *MultiTxSnapshotStack {
	return &MultiTxSnapshotStack{
		snapshots: make([]MultiTxSnapshot, 0),
		state:     state,
	}
}

// NewSnapshot creates a new snapshot and pushes it on top of the stack.
func (stack *MultiTxSnapshotStack) NewSnapshot() (*MultiTxSnapshot, error) {
	if len(stack.snapshots) > 0 && stack.snapshots[len(stack.snapshots)-1].invalid {
		return nil, errors.New("failed to create new multi-transaction snapshot - invalid snapshot found at head")
	}

	snap := newMultiTxSnapshot()
	stack.snapshots = append(stack.snapshots, snap)
	return &snap, nil
}

// Peek returns the snapshot at the top of the stack.
func (stack *MultiTxSnapshotStack) Peek() *MultiTxSnapshot {
	if len(stack.snapshots) == 0 {
		return nil
	}
	return &stack.snapshots[len(stack.snapshots)-1]
}

// Pop removes the snapshot at the top of the stack and returns it.
func (stack *MultiTxSnapshotStack) Pop() (*MultiTxSnapshot, error) {
	size := len(stack.snapshots)
	if size == 0 {
		return nil, errors.New("failed to revert multi-transaction snapshot - does not exist")
	}

	head := &stack.snapshots[size-1]
	stack.snapshots = stack.snapshots[:size-1]
	return head, nil
}

// Revert rewinds the changes from the head snapshot and removes it from the stack.
func (stack *MultiTxSnapshotStack) Revert() (*MultiTxSnapshot, error) {
	size := len(stack.snapshots)
	if size == 0 {
		return nil, errors.New("failed to revert multi-transaction snapshot - does not exist")
	}

	head := &stack.snapshots[size-1]
	if head.invalid {
		return nil, errors.New("failed to revert multi-transaction snapshot - invalid snapshot found")
	}

	head.revertState(stack.state)
	stack.snapshots = stack.snapshots[:size-1]
	return head, nil
}

// Commit merges the changes from the head snapshot with the previous snapshot and removes it from the stack.
func (stack *MultiTxSnapshotStack) Commit() (*MultiTxSnapshot, error) {
	if len(stack.snapshots) == 0 {
		return nil, errors.New("failed to commit multi-transaction snapshot - does not exist")
	}

	if len(stack.snapshots) == 1 {
		return stack.Pop()
	}

	var (
		head *MultiTxSnapshot
		err  error
	)
	if head, err = stack.Pop(); err != nil {
		return nil, err
	}

	current := stack.Peek()
	if err = current.Merge(head); err != nil {
		return nil, err
	}

	stack.snapshots[len(stack.snapshots)-1] = *current
	return head, nil
}

// Size returns the number of snapshots in the stack.
func (stack *MultiTxSnapshotStack) Size() int {
	return len(stack.snapshots)
}

// Invalidate invalidates the latest snapshot. This is used when state changes are committed to trie.
func (stack *MultiTxSnapshotStack) Invalidate() {
	// TODO: if latest snapshot is invalid, then all previous snapshots
	//   would also be invalidated, need to update logic to reflect that
	size := len(stack.snapshots)
	if size == 0 {
		return
	}

	head := stack.snapshots[size-1]
	head.invalid = true
	stack.snapshots = stack.snapshots[:0]
	stack.snapshots = append(stack.snapshots, head)
	//stack.snapshots[size-1].invalid = true
}

// UpdatePendingStatus updates the pending status for an address.
func (stack *MultiTxSnapshotStack) UpdatePendingStatus(address common.Address, pending, dirty bool) {
	if len(stack.snapshots) == 0 {
		return
	}

	current := stack.Peek()
	current.updatePendingStatus(address, pending, dirty)
	stack.snapshots[len(stack.snapshots)-1] = *current
}

// UpdatePendingStorage updates the pending storage for an address.
func (stack *MultiTxSnapshotStack) UpdatePendingStorage(address common.Address, key, value common.Hash, ok bool) {
	if len(stack.snapshots) == 0 {
		return
	}

	current := stack.Peek()
	current.updatePendingStorage(address, key, value, ok)
	stack.snapshots[len(stack.snapshots)-1] = *current
}

// UpdateFromJournal updates the snapshot with the changes from the journal.
func (stack *MultiTxSnapshotStack) UpdateFromJournal(journal *journal) {
	if len(stack.snapshots) == 0 {
		return
	}

	current := stack.Peek()
	current.updateFromJournal(journal)
	stack.snapshots[len(stack.snapshots)-1] = *current
}

// UpdateObjectDeleted updates the snapshot with the object deletion.
func (stack *MultiTxSnapshotStack) UpdateObjectDeleted(address common.Address, deleted bool) {
	if len(stack.snapshots) == 0 {
		return
	}

	current := stack.Peek()
	current.updateObjectDeleted(address, deleted)
	stack.snapshots[len(stack.snapshots)-1] = *current
}