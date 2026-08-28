package tracing

var HandleDB = map[string]*Handle{}

func Register(name string, h *Handle) {
	HandleDB[name] = h
}
