package model

// 返回给调用方的错误码，作为 connect.Error 的 message 透出。
// 同一类问题用同一个码，括号里是对应的 gRPC code。
const (
	// ---------- 请求参数（CodeInvalidArgument）----------

	// InvalidSmartGuid 设备 GUID 缺失或格式非法（本服务里 GUID 就是蓝牙 MAC）
	InvalidSmartGuid = "invalid_smart_guid"
	// NoSettingSpecified 设置请求里没指定要改哪一项
	NoSettingSpecified = "no_setting_specified"
	// UnknownContentType 纸条内容里有不认识的段类型，具体取值放 ErrDetail
	UnknownContentType = "unknown_content_type"
	// PaperContentEmpty 纸条内容为空
	PaperContentEmpty = "paper_content_empty"
	// InvalidImage 图片解不开或尺寸为空
	InvalidImage = "invalid_image"

	// ---------- 设备 ----------

	// DeviceNotFound 设备不存在，既没连上也不在连接中（CodeNotFound）
	DeviceNotFound = "device_not_found"
	// DeviceAlreadyConnected 设备已连接或正在连接（CodeAlreadyExists）
	DeviceAlreadyConnected = "device_already_connected"
	// DeviceConnNotReady 连接还没就绪（CodeUnavailable）
	DeviceConnNotReady = "device_conn_not_ready"
	// DeviceQueueFull 消息队列已满，拒绝新任务（CodeResourceExhausted）
	DeviceQueueFull = "device_queue_full"
	// DeviceKickFailed 踢出设备失败，底层原因放 ErrDetail（CodeInternal）
	DeviceKickFailed = "device_kick_failed"
	// DeviceSendFailed 向设备写数据失败，底层原因放 ErrDetail（CodeUnavailable）
	DeviceSendFailed = "device_send_failed"
)
