package status

// Code is the internal numeric representation of endpoint/instance health status.
// Boundary layers (proto, JSON, logs) use Parse and String.
type Code uint8

const (
	Unspecified Code = iota
	Healthy
	Degraded
	Ejected
	Probing
	Dead
	Unavailable
)

func Parse(s string) Code {
	switch s {
	case "", "HEALTHY":
		return Healthy
	case "DEGRADED":
		return Degraded
	case "EJECTED":
		return Ejected
	case "PROBING":
		return Probing
	case "DEAD":
		return Dead
	case "UNAVAILABLE":
		return Unavailable
	default:
		return Unspecified
	}
}

func (c Code) String() string {
	switch c {
	case Healthy:
		return "HEALTHY"
	case Degraded:
		return "DEGRADED"
	case Ejected:
		return "EJECTED"
	case Probing:
		return "PROBING"
	case Dead:
		return "DEAD"
	case Unavailable:
		return "UNAVAILABLE"
	default:
		return "HEALTHY"
	}
}

func Normalized(c Code) Code {
	if c == Unspecified {
		return Healthy
	}
	return c
}

func (c Code) Routable() bool {
	switch Normalized(c) {
	case Healthy, Degraded, Probing:
		return true
	default:
		return false
	}
}

func (c Code) NormalTraffic() bool {
	switch Normalized(c) {
	case Healthy, Degraded:
		return true
	default:
		return false
	}
}

func (c Code) IsProbing() bool {
	return c == Probing
}
