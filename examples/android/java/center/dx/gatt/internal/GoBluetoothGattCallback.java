package center.dx.gatt.internal;

/**
 * GoBluetoothGattCallback matches the adapter name derived by jni.NewProxy
 * for android.bluetooth.BluetoothGattCallback.
 */
public class GoBluetoothGattCallback extends GoGattCallback {
    public GoBluetoothGattCallback(long handlerID) {
        super(handlerID);
    }
}
