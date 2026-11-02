package signaling

import (
	"context"
	"sync"
)

// MemStore es un Store en memoria (servidor local y tests).
type MemStore struct {
	mu      sync.Mutex
	devices map[string]Device
	byDev   map[string]string // dispositivo → conexión
	byConn  map[string]string // conexión → dispositivo
}

func NewMemStore(devices ...Device) *MemStore {
	m := &MemStore{devices: map[string]Device{}, byDev: map[string]string{}, byConn: map[string]string{}}
	for _, d := range devices {
		m.devices[d.Name] = d
	}
	return m
}

func (m *MemStore) Device(_ context.Context, name string) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[name]
	if !ok {
		return Device{}, ErrNotFound
	}
	return d, nil
}

func (m *MemStore) Devices(context.Context) ([]DeviceStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []DeviceStatus
	for _, d := range m.devices {
		_, online := m.byDev[d.Name]
		out = append(out, DeviceStatus{Name: d.Name, Kind: d.Kind, Online: online})
	}
	return out, nil
}

func (m *MemStore) SetConnection(_ context.Context, device, connID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.byDev[device]; ok {
		delete(m.byConn, old) // un dispositivo, una conexión: la nueva reemplaza a la vieja
	}
	m.byDev[device] = connID
	m.byConn[connID] = device
	return nil
}

func (m *MemStore) ClearConnection(_ context.Context, connID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.byConn[connID]
	if !ok {
		return "", ErrNotFound
	}
	delete(m.byConn, connID)
	if m.byDev[d] == connID {
		delete(m.byDev, d)
	}
	return d, nil
}

func (m *MemStore) ConnectionOf(_ context.Context, device string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.byDev[device]
	if !ok {
		return "", ErrNotFound
	}
	return c, nil
}

func (m *MemStore) DeviceOf(_ context.Context, connID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.byConn[connID]
	if !ok {
		return "", ErrNotFound
	}
	return d, nil
}
