// Package native exposes the Vosk calls used by Rocket Pod, checking native
// allocation failures so a bad model cannot crash the server during a switch.
package native

/*
#cgo !windows LDFLAGS: -lvosk -ldl -lpthread
#cgo windows LDFLAGS: -lvosk -lpthread
#include <stdlib.h>
#include <vosk_api.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type VoskModel struct{ pointer *C.VoskModel }
type VoskRecognizer struct{ pointer *C.VoskRecognizer }

func SetLogLevel(level int) { C.vosk_set_log_level(C.int(level)) }

func NewModel(path string) (*VoskModel, error) {
	name := C.CString(path)
	defer C.free(unsafe.Pointer(name))
	pointer := C.vosk_model_new(name)
	if pointer == nil {
		return nil, fmt.Errorf("Vosk could not load %s; check model files and available RAM", path)
	}
	return &VoskModel{pointer}, nil
}

func (m *VoskModel) Free() {
	if m != nil && m.pointer != nil {
		C.vosk_model_free(m.pointer)
		m.pointer = nil
	}
}

func (m *VoskModel) FindWord(word string) int {
	text := C.CString(word)
	defer C.free(unsafe.Pointer(text))
	return int(C.vosk_model_find_word(m.pointer, text))
}

func NewRecognizer(model *VoskModel, rate float64) (*VoskRecognizer, error) {
	pointer := C.vosk_recognizer_new(model.pointer, C.float(rate))
	if pointer == nil {
		return nil, fmt.Errorf("Vosk could not create a recognizer; check available RAM")
	}
	return &VoskRecognizer{pointer}, nil
}

func NewRecognizerGrm(model *VoskModel, rate float64, grammar string) (*VoskRecognizer, error) {
	text := C.CString(grammar)
	defer C.free(unsafe.Pointer(text))
	pointer := C.vosk_recognizer_new_grm(model.pointer, C.float(rate), text)
	if pointer == nil {
		return nil, fmt.Errorf("Vosk could not create a grammar recognizer for this model")
	}
	return &VoskRecognizer{pointer}, nil
}

func (r *VoskRecognizer) Free() {
	if r != nil && r.pointer != nil {
		C.vosk_recognizer_free(r.pointer)
		r.pointer = nil
	}
}

func (r *VoskRecognizer) AcceptWaveform(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	return int(C.vosk_recognizer_accept_waveform(r.pointer, (*C.char)(unsafe.Pointer(&data[0])), C.int(len(data))))
}

func (r *VoskRecognizer) SetWords(enabled int) {
	C.vosk_recognizer_set_words(r.pointer, C.int(enabled))
}
func (r *VoskRecognizer) FinalResult() string {
	return C.GoString(C.vosk_recognizer_final_result(r.pointer))
}
