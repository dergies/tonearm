package callback

import "sync"

var (
	deletionCallbacks     = make(map[uintptr][]func())
	deletionCallbacksLock sync.RWMutex
)

func RegisterDeletionCallback(objectPtr uintptr, callback func()) {
	deletionCallbacksLock.Lock()
	defer deletionCallbacksLock.Unlock()
	deletionCallbacks[objectPtr] = append(deletionCallbacks[objectPtr], callback)
}

func executeDeletionCallbacks(objectPtr uintptr) {
	deletionCallbacksLock.RLock()
	defer deletionCallbacksLock.RUnlock()
	for _, callback := range deletionCallbacks[objectPtr] {
		callback()
	}
	delete(deletionCallbacks, objectPtr)
}
