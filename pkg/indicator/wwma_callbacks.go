// Code generated by "callbackgen -type WWMA"; DO NOT EDIT.

package indicator

import ()

func (inc *WWMA) OnUpdate(cb func(value float64)) {
	inc.UpdateCallbacks = append(inc.UpdateCallbacks, cb)
}

func (inc *WWMA) EmitUpdate(value float64) {
	for _, cb := range inc.UpdateCallbacks {
		cb(value)
	}
}