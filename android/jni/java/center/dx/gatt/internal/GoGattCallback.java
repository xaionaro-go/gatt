package center.dx.gatt.internal;

import android.bluetooth.BluetoothGatt;
import android.bluetooth.BluetoothGattCharacteristic;
import android.bluetooth.BluetoothGattDescriptor;
import center.dx.jni.internal.GoAbstractDispatch;

/**
 * GoGattCallback is a concrete subclass of BluetoothGattCallback
 * that delegates all method calls to GoAbstractDispatch.invoke().
 */
public class GoGattCallback extends android.bluetooth.BluetoothGattCallback {
    private final long handlerID;

    public GoGattCallback(long handlerID) {
        this.handlerID = handlerID;
    }

    @Override
    public void onConnectionStateChange(BluetoothGatt gatt, int status, int newState) {
        GoAbstractDispatch.invoke(handlerID, "onConnectionStateChange",
            new Object[]{gatt, status, newState});
    }

    @Override
    public void onServicesDiscovered(BluetoothGatt gatt, int status) {
        GoAbstractDispatch.invoke(handlerID, "onServicesDiscovered",
            new Object[]{gatt, status});
    }

    @Override
    public void onCharacteristicRead(BluetoothGatt gatt, BluetoothGattCharacteristic characteristic, int status) {
        GoAbstractDispatch.invoke(handlerID, "onCharacteristicRead",
            new Object[]{gatt, characteristic, status});
    }

    @Override
    public void onCharacteristicWrite(BluetoothGatt gatt, BluetoothGattCharacteristic characteristic, int status) {
        GoAbstractDispatch.invoke(handlerID, "onCharacteristicWrite",
            new Object[]{gatt, characteristic, status});
    }

    @Override
    public void onCharacteristicChanged(BluetoothGatt gatt, BluetoothGattCharacteristic characteristic) {
        GoAbstractDispatch.invoke(handlerID, "onCharacteristicChanged",
            new Object[]{gatt, characteristic});
    }

    @Override
    public void onDescriptorRead(BluetoothGatt gatt, BluetoothGattDescriptor descriptor, int status) {
        GoAbstractDispatch.invoke(handlerID, "onDescriptorRead",
            new Object[]{gatt, descriptor, status});
    }

    @Override
    public void onDescriptorWrite(BluetoothGatt gatt, BluetoothGattDescriptor descriptor, int status) {
        GoAbstractDispatch.invoke(handlerID, "onDescriptorWrite",
            new Object[]{gatt, descriptor, status});
    }

    @Override
    public void onReadRemoteRssi(BluetoothGatt gatt, int rssi, int status) {
        GoAbstractDispatch.invoke(handlerID, "onReadRemoteRssi",
            new Object[]{gatt, rssi, status});
    }

    @Override
    public void onMtuChanged(BluetoothGatt gatt, int mtu, int status) {
        GoAbstractDispatch.invoke(handlerID, "onMtuChanged",
            new Object[]{gatt, mtu, status});
    }

    @Override
    public void onServiceChanged(BluetoothGatt gatt) {
        GoAbstractDispatch.invoke(handlerID, "onServiceChanged",
            new Object[]{gatt});
    }
}
