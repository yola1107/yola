// Package message owns wire values shared by the example applications.
package message

const (
	AuthTokenPrefix   = "example-token:"
	AnnouncementTopic = "topic.gateway.announcement.v1"

	EchoCommand         int32 = 1
	WhotLeaveCommand    int32 = 2
	WhotEnterCommand    int32 = 3
	WhotPushCommand     int32 = 4
	AnnouncementCommand int32 = 10005
)
