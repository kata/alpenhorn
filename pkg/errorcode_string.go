// Code generated by "stringer -type=ErrorCode"; DO NOT EDIT.

package pkg

import "fmt"

const _ErrorCode_name = "ErrBadRequestJSONErrDatabaseErrorErrInvalidUsernameErrInvalidLoginKeyErrNotRegisteredErrNotVerifiedErrAlreadyRegisteredErrRegistrationInProgressErrSendingEmailErrRoundNotFoundErrInvalidUserLongTermKeyErrInvalidSignatureErrInvalidTokenErrExpiredTokenErrUnauthorizedErrBadCommitmentErrUnknown"

var _ErrorCode_index = [...]uint16{0, 17, 33, 51, 69, 85, 99, 119, 144, 159, 175, 200, 219, 234, 249, 264, 280, 290}

func (i ErrorCode) String() string {
	i -= 1
	if i < 0 || i >= ErrorCode(len(_ErrorCode_index)-1) {
		return fmt.Sprintf("ErrorCode(%d)", i+1)
	}
	return _ErrorCode_name[_ErrorCode_index[i]:_ErrorCode_index[i+1]]
}
