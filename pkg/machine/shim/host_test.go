package shim

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.podman.io/podman/v6/pkg/machine/define"
	"go.podman.io/podman/v6/pkg/machine/vmconfigs"
)

func Test_validateDestinationPaths(t *testing.T) {
	tests := []struct {
		name    string
		dest    string
		wantErr bool
	}{
		{
			name:    "Expect fail - /tmp",
			dest:    "/tmp",
			wantErr: true,
		},
		{
			name:    "Expect fail trailing /",
			dest:    "/tmp/",
			wantErr: true,
		},
		{
			name:    "Expect fail double /",
			dest:    "//tmp",
			wantErr: true,
		},
		{
			name:    "/var should fail",
			dest:    "/var",
			wantErr: true,
		},
		{
			name:    "/etc should fail",
			dest:    "/etc",
			wantErr: true,
		},
		{
			name:    "/tmp subdir OK",
			dest:    "/tmp/foobar",
			wantErr: false,
		},
		{
			name:    "/foobar OK",
			dest:    "/foobar",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDestinationPaths(tt.dest)
			if tt.wantErr {
				assert.ErrorContainsf(t, err, "onsider another location or a subdirectory of an existing location", "illegal mount target")
			} else {
				assert.NoError(t, err, "mounts to subdirs or non-critical dirs should succeed")
			}
		})
	}
}

// mockProvider is a minimal mock for testing Starting-state recovery
type mockProvider struct {
	state define.Status
	err   error
}

func (m *mockProvider) State(_ *vmconfigs.MachineConfig, _ bool) (define.Status, error) {
	return m.state, m.err
}

// Implement other required VMProvider methods as no-ops
func (m *mockProvider) CreateVM(_ define.CreateVMOpts, _ *vmconfigs.MachineConfig, _ interface{}) error {
	return nil
}

func (m *mockProvider) StartVM(_ *vmconfigs.MachineConfig) (func() error, func() error, error) {
	return func() error { return nil }, func() error { return nil }, nil
}

func (m *mockProvider) StopVM(_ *vmconfigs.MachineConfig, _ bool) error {
	return nil
}

func (m *mockProvider) PostStartNetworking(_ *vmconfigs.MachineConfig, _ bool) error {
	return nil
}

func (m *mockProvider) MountVolumesToVM(_ *vmconfigs.MachineConfig, _ bool) error {
	return nil
}

func (m *mockProvider) StopHostNetworking(_ *vmconfigs.MachineConfig, _ define.VMType) error {
	return nil
}

func (m *mockProvider) Remove(_ *vmconfigs.MachineConfig) ([]string, func() error, error) {
	return []string{}, func() error { return nil }, nil
}

func (m *mockProvider) Update(_ *vmconfigs.MachineConfig, _ bool) error {
	return nil
}

func (m *mockProvider) SetCPUs(_ *vmconfigs.MachineConfig, _ uint) error {
	return nil
}

func (m *mockProvider) SetMemory(_ *vmconfigs.MachineConfig, _ uint) error {
	return nil
}

func (m *mockProvider) SetDisk(_ *vmconfigs.MachineConfig, _ uint) error {
	return nil
}

func (m *mockProvider) IsRunning() bool {
	return m.state == define.Running
}

func (m *mockProvider) VMType() define.VMType {
	return define.QemuVirt
}

func (m *mockProvider) RequireExclusiveActive() bool {
	return false
}

func (m *mockProvider) UseProviderNetworkSetup() bool {
	return false
}

func (m *mockProvider) UserModeNetworkEnabled(_ *vmconfigs.MachineConfig) bool {
	return false
}

// mockWriteCounter tracks how many times mc.Write() is called
type mockWriteCounter struct {
	writeCount int
}

func (m *mockWriteCounter) Write() error {
	m.writeCount++
	return nil
}

// mockMachineConfig wraps MachineConfig to intercept Write() calls
type mockMachineConfig struct {
	*vmconfigs.MachineConfig
	writeCounter *mockWriteCounter
}

func (m *mockMachineConfig) Write() error {
	if m.writeCounter != nil {
		return m.writeCounter.Write()
	}
	return m.MachineConfig.Write()
}

func Test_StaleStartingStateRecovery(t *testing.T) {
	t.Run("Starting=true + State=Stopped clears Starting", func(t *testing.T) {
		writeCounter := &mockWriteCounter{}
		mc := &mockMachineConfig{
			MachineConfig: &vmconfigs.MachineConfig{
				Starting: true,
			},
			writeCounter: writeCounter,
		}
		mp := &mockProvider{state: define.Stopped}

		recoverStaleStartingState(mc.MachineConfig, mp)

		assert.False(t, mc.Starting, "Starting should be cleared when machine is stopped")
		assert.Equal(t, 1, writeCounter.writeCount, "mc.Write() should be called once to persist the change")
	})

	t.Run("Starting=true + State=Running preserves Starting", func(t *testing.T) {
		writeCounter := &mockWriteCounter{}
		mc := &mockMachineConfig{
			MachineConfig: &vmconfigs.MachineConfig{
				Starting: true,
			},
			writeCounter: writeCounter,
		}
		mp := &mockProvider{state: define.Running}

		recoverStaleStartingState(mc.MachineConfig, mp)

		assert.True(t, mc.Starting, "Starting should be preserved when machine is running")
		assert.Equal(t, 0, writeCounter.writeCount, "mc.Write() should not be called when Starting is preserved")
	})

	t.Run("Starting=true + State=Starting preserves Starting", func(t *testing.T) {
		writeCounter := &mockWriteCounter{}
		mc := &mockMachineConfig{
			MachineConfig: &vmconfigs.MachineConfig{
				Starting: true,
			},
			writeCounter: writeCounter,
		}
		mp := &mockProvider{state: define.Starting}

		recoverStaleStartingState(mc.MachineConfig, mp)

		assert.True(t, mc.Starting, "Starting should be preserved when provider reports Starting")
		assert.Equal(t, 0, writeCounter.writeCount, "mc.Write() should not be called when Starting is preserved")
	})

	t.Run("Starting=true + State=Unknown preserves Starting", func(t *testing.T) {
		writeCounter := &mockWriteCounter{}
		mc := &mockMachineConfig{
			MachineConfig: &vmconfigs.MachineConfig{
				Starting: true,
			},
			writeCounter: writeCounter,
		}
		mp := &mockProvider{state: define.Unknown}

		recoverStaleStartingState(mc.MachineConfig, mp)

		assert.True(t, mc.Starting, "Starting should be preserved when provider reports Unknown")
		assert.Equal(t, 0, writeCounter.writeCount, "mc.Write() should not be called when Starting is preserved")
	})

	t.Run("Starting=true + State() error preserves Starting", func(t *testing.T) {
		writeCounter := &mockWriteCounter{}
		mc := &mockMachineConfig{
			MachineConfig: &vmconfigs.MachineConfig{
				Starting: true,
			},
			writeCounter: writeCounter,
		}
		mp := &mockProvider{state: define.Stopped, err: assert.AnError}

		recoverStaleStartingState(mc.MachineConfig, mp)

		assert.True(t, mc.Starting, "Starting should be preserved when State() returns an error")
		assert.Equal(t, 0, writeCounter.writeCount, "mc.Write() should not be called when State() errors")
	})

	t.Run("Starting=false remains false", func(t *testing.T) {
		writeCounter := &mockWriteCounter{}
		mc := &mockMachineConfig{
			MachineConfig: &vmconfigs.MachineConfig{
				Starting: false,
			},
			writeCounter: writeCounter,
		}
		mp := &mockProvider{state: define.Stopped}

		recoverStaleStartingState(mc.MachineConfig, mp)

		assert.False(t, mc.Starting, "Starting should remain false when already false")
		assert.Equal(t, 0, writeCounter.writeCount, "mc.Write() should not be called when Starting is already false")
	})
}
