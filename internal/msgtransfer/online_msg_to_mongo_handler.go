package msgtransfer

import (
	"context"

	"github.com/OpenIMSDK/Open-IM-Server/pkg/proto/sdkws"

	"github.com/OpenIMSDK/Open-IM-Server/pkg/common/config"
	"github.com/OpenIMSDK/Open-IM-Server/pkg/common/constant"
	"github.com/OpenIMSDK/Open-IM-Server/pkg/common/db/controller"
	kfk "github.com/OpenIMSDK/Open-IM-Server/pkg/common/kafka"
	"github.com/OpenIMSDK/Open-IM-Server/pkg/common/log"
	pbMsg "github.com/OpenIMSDK/Open-IM-Server/pkg/proto/msg"
	"github.com/Shopify/sarama"
	"google.golang.org/protobuf/proto"
)

type OnlineHistoryMongoConsumerHandler struct {
	historyConsumerGroup *kfk.MConsumerGroup
	msgDatabase          controller.CommonMsgDatabase
}

func NewOnlineHistoryMongoConsumerHandler(database controller.CommonMsgDatabase) *OnlineHistoryMongoConsumerHandler {
	mc := &OnlineHistoryMongoConsumerHandler{
		historyConsumerGroup: kfk.NewMConsumerGroup(&kfk.MConsumerGroupConfig{KafkaVersion: sarama.V2_0_0_0,
			OffsetsInitial: sarama.OffsetNewest, IsReturnErr: false}, []string{config.Config.Kafka.MsgToMongo.Topic},
			config.Config.Kafka.MsgToMongo.Addr, config.Config.Kafka.ConsumerGroupID.MsgToMongo),
		msgDatabase: database,
	}
	return mc
}

func (mc *OnlineHistoryMongoConsumerHandler) handleChatWs2Mongo(ctx context.Context, cMsg *sarama.ConsumerMessage, key string, session sarama.ConsumerGroupSession) {
	msg := cMsg.Value
	msgFromMQ := pbMsg.MsgDataToMongoByMQ{}
	err := proto.Unmarshal(msg, &msgFromMQ)
	if err != nil {
		log.ZError(ctx, "unmarshall failed", err, "key", key, "len", len(msg))
		return
	}
	if len(msgFromMQ.MsgData) == 0 {
		log.ZError(ctx, "msgFromMQ.MsgData is empty", nil, "cMsg", cMsg)
		return
	}
	log.ZInfo(ctx, "mongo consumer recv msg", "msgs", msgFromMQ.MsgData)
	err = mc.msgDatabase.BatchInsertChat2DB(ctx, msgFromMQ.ConversationID, msgFromMQ.MsgData, msgFromMQ.LastSeq)
	if err != nil {
		log.ZError(ctx, "single data insert to mongo err", err, "msg", msgFromMQ.MsgData, "conversationID", msgFromMQ.ConversationID)
	}
	err = mc.msgDatabase.DeleteMessageFromCache(ctx, msgFromMQ.ConversationID, msgFromMQ.MsgData)
	if err != nil {
		log.ZError(ctx, "remove cache msg from redis err", err, "msg", msgFromMQ.MsgData, "conversationID", msgFromMQ.ConversationID)
	}
	for _, v := range msgFromMQ.MsgData {
		if v.ContentType == constant.DeleteMessageNotification {
			deleteMessageTips := sdkws.DeleteMessageTips{}
			err := proto.Unmarshal(v.Content, &deleteMessageTips)
			if err != nil {
				log.ZError(ctx, "tips unmarshal err:", err, "msg", msg)
				continue
			}
			if totalUnExistSeqs, err := mc.msgDatabase.DelMsgBySeqs(ctx, deleteMessageTips.UserID, deleteMessageTips.Seqs); err != nil {
				log.ZError(ctx, "DelMsgBySeqs", err, "userIDs", deleteMessageTips.UserID, "seqs", deleteMessageTips.Seqs, "totalUnExistSeqs", totalUnExistSeqs)
			}
		}
	}

}

func (OnlineHistoryMongoConsumerHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (OnlineHistoryMongoConsumerHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }
func (mc *OnlineHistoryMongoConsumerHandler) ConsumeClaim(sess sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error { // a instance in the consumer group
	log.ZDebug(context.Background(), "online new session msg come", "highWaterMarkOffset",
		claim.HighWaterMarkOffset(), "topic", claim.Topic(), "partition", claim.Partition())
	for msg := range claim.Messages() {
		ctx := mc.historyConsumerGroup.GetContextFromMsg(msg)
		if len(msg.Value) != 0 {
			mc.handleChatWs2Mongo(ctx, msg, string(msg.Key), sess)
		} else {
			log.ZError(ctx, "mongo msg get from kafka but is nil", nil, "conversationID", msg.Key)
		}
		sess.MarkMessage(msg, "")
	}
	return nil
}
