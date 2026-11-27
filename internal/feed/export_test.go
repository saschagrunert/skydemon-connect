package feed

// FailureTexts are the failures the tray shows, for a test against its
// tooltip, which can't import tray from inside this package.
func FailureTexts() []string {
	return []string{udpFailsText, noTargetsText, serialFails(""), serialFails(`\\.\COM255`)}
}
