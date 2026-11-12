package preflight

import "strings"

// Identity names the SaladCloud node and instance a run happened on, so events
// from many nodes can be compared without relying on the log pipeline's labels.
type Identity struct{ MachineID, InstanceID, ContainerGroupID string }

// Identify reads the identifiers SaladCloud sets in every container. A value
// that does not look like an identifier is dropped rather than logged.
func Identify(env []string) Identity {
	id := Identity{}
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || v == "" || len(v) > 128 || strings.IndexFunc(v, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':')
		}) >= 0 {
			continue
		}
		switch k {
		case "SALAD_MACHINE_ID":
			id.MachineID = v
		case "SALAD_INSTANCE_ID":
			id.InstanceID = v
		case "SALAD_CONTAINER_GROUP_ID":
			id.ContainerGroupID = v
		}
	}
	return id
}

// Attach returns an Emit that stamps the identity on every event.
func (id Identity) Attach(emit Emit) Emit {
	return func(e Event) {
		e.MachineID, e.InstanceID, e.ContainerGroupID = id.MachineID, id.InstanceID, id.ContainerGroupID
		emit(e)
	}
}
