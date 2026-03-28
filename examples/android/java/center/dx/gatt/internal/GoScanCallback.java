package center.dx.gatt.internal;

import android.bluetooth.le.ScanCallback;
import android.bluetooth.le.ScanResult;
import center.dx.jni.internal.GoAbstractDispatch;
import java.util.List;

/**
 * GoScanCallback is a concrete subclass of the abstract ScanCallback
 * that delegates all method calls to GoAbstractDispatch.invoke().
 * This is needed because Java Proxy only supports interfaces, not
 * abstract classes.
 */
public class GoScanCallback extends ScanCallback {
    private final long handlerID;

    public GoScanCallback(long handlerID) {
        this.handlerID = handlerID;
    }

    @Override
    public void onScanResult(int callbackType, ScanResult result) {
        GoAbstractDispatch.invoke(handlerID, "onScanResult",
            new Object[]{callbackType, result});
    }

    @Override
    public void onScanFailed(int errorCode) {
        GoAbstractDispatch.invoke(handlerID, "onScanFailed",
            new Object[]{errorCode});
    }

    @Override
    public void onBatchScanResults(List<ScanResult> results) {
        GoAbstractDispatch.invoke(handlerID, "onBatchScanResults",
            new Object[]{results});
    }
}
