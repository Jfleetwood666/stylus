//
// Copyright 2021-2022, Offchain Labs, Inc. All rights reserved.
//

package relay

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/offchainlabs/nitro/arbstate"
	"github.com/offchainlabs/nitro/arbutil"
	"github.com/offchainlabs/nitro/broadcastclient"
	"github.com/offchainlabs/nitro/broadcaster"
	"github.com/offchainlabs/nitro/wsbroadcastserver"
)

type Relay struct {
	broadcastClients            []*broadcastclient.BroadcastClient
	broadcaster                 *broadcaster.Broadcaster
	confirmedSequenceNumberChan chan arbutil.MessageIndex
	messageChan                 chan broadcastFeedMessage
}

type broadcastFeedMessage struct {
	message        arbstate.MessageWithMetadata
	sequenceNumber arbutil.MessageIndex
}

type RelayMessageQueue struct {
	queue chan broadcastFeedMessage
}

func (q *RelayMessageQueue) AddMessages(pos arbutil.MessageIndex, force bool, messages []arbstate.MessageWithMetadata) error {
	/*
		var broadcastFeedMessages []*broadcaster.BroadcastFeedMessage
		for i, message := range messages {
			broadcastFeedMessages = append(broadcastFeedMessages, &broadcaster.BroadcastFeedMessage{
				SequenceNumber: pos + arbutil.MessageIndex(i),
				Message:        message,
			})
		}

		q.queue <- broadcaster.BroadcastMessage{1, broadcastFeedMessages, nil}
	*/
	for i, message := range messages {
		q.queue <- broadcastFeedMessage{
			sequenceNumber: pos + arbutil.MessageIndex(i),
			message:        message,
		}
	}

	return nil
}

func NewRelay(serverConf wsbroadcastserver.BroadcasterConfig, clientConf broadcastclient.BroadcastClientConfig) *Relay {
	var broadcastClients []*broadcastclient.BroadcastClient
	// confirmedAccumulatorChan := make(chan common.Hash, 1)
	// for _, address := range settings.Input.URLs {

	q := RelayMessageQueue{make(chan broadcastFeedMessage, 100)}

	client := broadcastclient.NewBroadcastClient(clientConf.URL, nil, clientConf.Timeout, &q)
	client.ConfirmedSequenceNumberListener = make(chan arbutil.MessageIndex, 10)

	broadcastClients = append(broadcastClients, client)
	// }
	return &Relay{
		broadcaster:                 broadcaster.NewBroadcaster(serverConf),
		broadcastClients:            broadcastClients,
		confirmedSequenceNumberChan: client.ConfirmedSequenceNumberListener,
		messageChan:                 q.queue,
	}
}

const RECENT_FEED_ITEM_TTL time.Duration = time.Second * 10

func (r *Relay) Start(ctx context.Context) (chan bool, error) {
	done := make(chan bool)

	err := r.broadcaster.Start(ctx)
	if err != nil {
		return nil, errors.New("broadcast unable to start")
	}

	for _, client := range r.broadcastClients {
		client.Start(ctx)
	}

	recentFeedItems := make(map[arbutil.MessageIndex]time.Time)
	go func() {
		defer func() {
			done <- true
		}()
		recentFeedItemsCleanup := time.NewTicker(RECENT_FEED_ITEM_TTL)
		defer recentFeedItemsCleanup.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-r.messageChan:
				if recentFeedItems[msg.sequenceNumber] != (time.Time{}) {
					continue
				}
				recentFeedItems[msg.sequenceNumber] = time.Now()
				r.broadcaster.BroadcastSingle(msg.message, msg.sequenceNumber)
			case cs := <-r.confirmedSequenceNumberChan:
				r.broadcaster.Confirm(cs)
			case <-recentFeedItemsCleanup.C:
				// Clear expired items from recentFeedItems
				recentFeedItemExpiry := time.Now().Add(-RECENT_FEED_ITEM_TTL)
				for acc, created := range recentFeedItems {
					if created.Before(recentFeedItemExpiry) {
						delete(recentFeedItems, acc)
					}
				}
			}
		}
	}()

	return done, nil
}

func (r *Relay) GetListenerAddr() net.Addr {
	return r.broadcaster.ListenerAddr()
}

func (r *Relay) Stop() {
	for _, client := range r.broadcastClients {
		client.Close()
	}
	r.broadcaster.StopAndWait()
}
