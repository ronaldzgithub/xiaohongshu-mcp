package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalActionsFailClosed(t *testing.T) {
	t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", "")
	service := NewXiaohongshuService()
	ctx := context.Background()

	actions := map[string]func() error{
		"publish content": func() error {
			_, err := service.PublishContent(ctx, &PublishRequest{})
			return err
		},
		"publish video": func() error {
			_, err := service.PublishVideo(ctx, &PublishVideoRequest{})
			return err
		},
		"post comment": func() error {
			_, err := service.PostCommentToFeed(ctx, "feed", "token", "comment")
			return err
		},
		"like feed": func() error {
			_, err := service.LikeFeed(ctx, "feed", "token")
			return err
		},
		"unlike feed": func() error {
			_, err := service.UnlikeFeed(ctx, "feed", "token")
			return err
		},
		"favorite feed": func() error {
			_, err := service.FavoriteFeed(ctx, "feed", "token")
			return err
		},
		"unfavorite feed": func() error {
			_, err := service.UnfavoriteFeed(ctx, "feed", "token")
			return err
		},
		"reply comment": func() error {
			_, err := service.ReplyCommentToFeed(ctx, "feed", "token", "comment", "user", "reply")
			return err
		},
		"like notification": func() error {
			_, err := service.LikeNotification(ctx, "comment", false)
			return err
		},
		"reply notification": func() error {
			_, err := service.ReplyNotification(ctx, "comment", "reply")
			return err
		},
	}

	for name, action := range actions {
		t.Run(name, func(t *testing.T) {
			err := action()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "外部动作默认关闭")
		})
	}
}

func TestExternalActionsRequireExplicitAffirmativeValue(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "1", "yes", "on"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", value)
			assert.True(t, externalActionsEnabled())
		})
	}

	for _, value := range []string{"", "false", "0", "enabled", "maybe"} {
		t.Run("closed_"+value, func(t *testing.T) {
			t.Setenv("XHS_ENABLE_EXTERNAL_ACTIONS", value)
			assert.False(t, externalActionsEnabled())
		})
	}
}
