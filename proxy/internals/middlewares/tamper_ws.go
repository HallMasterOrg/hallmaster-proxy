package middlewares

// This function deals with the message received from Discord
func TamperOutgoing(message []byte) ([]byte, error) {
	return message, nil
}

// This function deals with the message sent to Discord
func TamperIncoming(message []byte) ([]byte, error) {
	return message, nil
}
