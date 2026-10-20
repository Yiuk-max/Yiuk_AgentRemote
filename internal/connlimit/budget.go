package connlimit

// Budget bounds sockets accepted across the control listener and all room listeners.
type Budget struct {
	slots chan struct{}
}

func New(max int) *Budget {
	if max < 1 {
		max = 1
	}
	return &Budget{slots: make(chan struct{}, max)}
}

func (b *Budget) TryAcquire() bool {
	select {
	case b.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (b *Budget) Release() {
	select {
	case <-b.slots:
	default:
	}
}
