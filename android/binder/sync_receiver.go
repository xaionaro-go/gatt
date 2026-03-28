package binder

import (
	"context"
	"fmt"

	binderpkg "github.com/AndroidGoLab/binder/binder"
	"github.com/AndroidGoLab/binder/parcel"
)

const descriptorIResultReceiver = "com.android.internal.os.IResultReceiver"

// Bundle value type constants from android.os.BaseBundle.
const (
	bundleValInteger = 1
	bundleValBoolean = 9
	bundleValIBinder = 15
	bundleValNull    = -1
	bundleMagic      = 0x4C444E42 // "BNDL" in little-endian
)

// syncResult holds the parsed result from an IResultReceiver.send() callback.
type syncResult struct {
	code         int32
	binderHandle uint32
	intValue     int32
	hasBinder    bool
	hasInt       bool
	err          error
}

// syncReceiver implements IResultReceiver as a local binder stub,
// receiving asynchronous results from Android system services.
// This is the production equivalent of the pattern used in the binder E2E tests.
type syncReceiver struct {
	stub     *binderpkg.StubBinder
	resultCh chan syncResult
}

func newSyncReceiver() *syncReceiver {
	r := &syncReceiver{
		resultCh: make(chan syncResult, 1),
	}
	r.stub = binderpkg.NewStubBinder(r)
	return r
}

func (r *syncReceiver) AsBinder() binderpkg.IBinder {
	return r.stub
}

// writeToParcel writes the receiver as a SynchronousResultReceiver parcelable.
// On API 33+, typed parcelables use a size envelope: non-null marker,
// then a size-prefixed block containing the IResultReceiver binder.
func (r *syncReceiver) writeToParcel(
	ctx context.Context,
	p *parcel.Parcel,
	transport binderpkg.Transport,
) {
	p.WriteInt32(1) // non-null marker
	headerPos := parcel.WriteParcelableHeader(p)
	binderpkg.WriteBinderToParcel(ctx, p, r.AsBinder(), transport)
	parcel.WriteParcelableFooter(p, headerPos)
}

func (r *syncReceiver) Descriptor() string {
	return descriptorIResultReceiver
}

// OnTransaction handles the IResultReceiver.send(resultCode, Bundle) call.
func (r *syncReceiver) OnTransaction(
	ctx context.Context,
	code binderpkg.TransactionCode,
	data *parcel.Parcel,
) (_reply *parcel.Parcel, _err error) {
	defer func() {
		if _err != nil {
			r.resultCh <- syncResult{err: _err}
		}
	}()

	if _, err := data.ReadInterfaceToken(); err != nil {
		return nil, fmt.Errorf("reading interface token: %w", err)
	}

	resultCode, err := data.ReadInt32()
	if err != nil {
		return nil, fmt.Errorf("reading resultCode: %w", err)
	}

	nullFlag, err := data.ReadInt32()
	if err != nil {
		return nil, fmt.Errorf("reading null flag: %w", err)
	}

	res := syncResult{code: resultCode}

	if nullFlag != 0 {
		if err := r.parseBundle(data, &res); err != nil {
			return nil, fmt.Errorf("parsing bundle: %w", err)
		}
	}

	r.resultCh <- res
	return nil, nil
}

// parseBundle reads an Android Bundle and extracts the first binder/int value.
func (r *syncReceiver) parseBundle(data *parcel.Parcel, res *syncResult) error {
	if _, err := data.ReadInt32(); err != nil {
		return fmt.Errorf("reading bundle data length: %w", err)
	}

	magic, err := data.ReadInt32()
	if err != nil {
		return fmt.Errorf("reading bundle magic: %w", err)
	}
	if magic != bundleMagic {
		return fmt.Errorf("unexpected bundle magic: 0x%08X", magic)
	}

	entryCount, err := data.ReadInt32()
	if err != nil {
		return fmt.Errorf("reading entry count: %w", err)
	}

	for i := int32(0); i < entryCount; i++ {
		if _, err := data.ReadString16(); err != nil {
			return fmt.Errorf("reading bundle key [%d]: %w", i, err)
		}

		valType, err := data.ReadInt32()
		if err != nil {
			return fmt.Errorf("reading bundle value type [%d]: %w", i, err)
		}

		switch valType {
		case bundleValInteger:
			v, err := data.ReadInt32()
			if err != nil {
				return fmt.Errorf("reading VAL_INTEGER [%d]: %w", i, err)
			}
			if !res.hasInt && !res.hasBinder {
				res.intValue = v
				res.hasInt = true
			}

		case bundleValBoolean:
			v, err := data.ReadInt32()
			if err != nil {
				return fmt.Errorf("reading VAL_BOOLEAN [%d]: %w", i, err)
			}
			if !res.hasInt && !res.hasBinder {
				res.intValue = v
				res.hasInt = true
			}

		case bundleValIBinder:
			handle, err := data.ReadStrongBinder()
			if err != nil {
				return fmt.Errorf("reading VAL_IBINDER [%d]: %w", i, err)
			}
			if !res.hasBinder {
				res.binderHandle = handle
				res.hasBinder = true
			}

		case bundleValNull:
			// nothing to read

		default:
			return fmt.Errorf("unsupported bundle value type %d at entry %d", valType, i)
		}
	}

	return nil
}

// awaitBinder waits for a binder handle result from the receiver.
// The context controls the timeout.
func (r *syncReceiver) awaitBinder(
	ctx context.Context,
	transport binderpkg.VersionAwareTransport,
) (binderpkg.IBinder, error) {
	select {
	case res := <-r.resultCh:
		if res.err != nil {
			return nil, res.err
		}
		if !res.hasBinder {
			return nil, nil
		}
		return binderpkg.NewProxyBinder(transport, binderpkg.CallerIdentity{}, res.binderHandle), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// awaitInt waits for an int32 result from the receiver.
// The context controls the timeout.
func (r *syncReceiver) awaitInt(ctx context.Context) (int32, error) {
	select {
	case res := <-r.resultCh:
		if res.err != nil {
			return 0, res.err
		}
		if !res.hasInt {
			return 0, fmt.Errorf("expected int result but got none")
		}
		return res.intValue, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
