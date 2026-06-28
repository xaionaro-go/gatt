package jni

import "github.com/xaionaro-go/gatt"

func advertisementFromScanRecord(
	name string,
	raw []byte,
) (*gatt.Advertisement, error) {
	fallback := &gatt.Advertisement{
		LocalName: name,
	}
	if len(raw) == 0 {
		return fallback, nil
	}

	adv, err := gatt.ParseAdvertisement(raw)
	if err != nil {
		return fallback, err
	}
	if adv.LocalName == "" {
		adv.LocalName = name
	}
	return adv, nil
}
