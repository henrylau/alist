package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	stdpath "path"
	"strconv"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/internal/sign"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gotd/contrib/bg"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
	"github.com/patrickmn/go-cache"
	log "github.com/sirupsen/logrus"
)

type AuthFlow struct {
	driver *Telegram
}

func (f *AuthFlow) Phone(ctx context.Context) (string, error) {
	return f.driver.PhoneNumber, nil
}

func (f *AuthFlow) SignUp(ctx context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("not implemented")
}
func (f *AuthFlow) Password(ctx context.Context) (string, error) {
	return "", errors.New("not implemented")
}

func (f *AuthFlow) AcceptTermsOfService(ctx context.Context, tos tg.HelpTermsOfService) error {
	return &auth.SignUpRequired{TermsOfService: tos}
}

func (f *AuthFlow) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return f.driver.AuthCode, nil
}

type Telegram struct {
	model.Storage
	Addition
	client       *telegram.Client
	stop         *bg.StopFunc
	peersManager *peers.Manager
	cache        *cache.Cache
	dcClient     *tg.Client
}

func (d *Telegram) Config() driver.Config {
	return config
}

func (d *Telegram) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Telegram) Init(ctx context.Context) error {
	authFlow := auth.NewFlow(
		&AuthFlow{
			driver: d,
		},
		auth.SendCodeOptions{},
	)

	id, _ := strconv.Atoi(d.Addition.ApiId)

	d.cache = cache.New(5*time.Minute, 10*time.Minute)

	d.client = telegram.NewClient(id, d.Addition.ApiHash, telegram.Options{
		SessionStorage: d,
		Device: telegram.DeviceConfig{
			DeviceModel:    "Alist",
			SystemVersion:  "Windows 10",
			AppVersion:     "4.2.4 x64",
			LangCode:       "en",
			SystemLangCode: "en-US",
			LangPack:       "tdesktop",
		},
		RetryInterval: time.Second,
		MaxRetries:    10,
		DialTimeout:   10 * time.Second,
	})

	log.Debug("Init Telegram")

	// bg.Connect will call Run in background.
	// Call stop() to disconnect and release resources.
	if d.stop == nil {
		stop, err := bg.Connect(d.client)
		if err != nil {
			return err
		}
		// defer func() { _ = stop() }()
		d.stop = &stop
	}

	status, err := d.client.Auth().Status(ctx)

	if err != nil {
		return err
	}

	if !status.Authorized {
		d.GetStorage().SetStatus("Unauthorized")
		op.MustSaveDriverStorage(d)
	}

	if err := d.client.Auth().IfNecessary(ctx, authFlow); err != nil {
		fmt.Print(err)
	}

	invoker, _ := d.client.Pool(10)
	d.dcClient = tg.NewClient(invoker)
	d.peersManager = peers.Options{Storage: &peers.InmemoryStorage{}}.Build(d.client.API())

	return nil
}

func (d *Telegram) Drop(ctx context.Context) error {
	return nil
}

func (d *Telegram) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	// TODO return the files list
	var files []model.Obj

	dirId := dir.GetID()

	// list for root
	if dirId == "" {
		if result, found := d.cache.Get("root"); found {
			files, _ := result.(*[]model.Obj)
			return *files, nil
		}
		dialogs, err := query.GetDialogs(d.client.API()).BatchSize(100).Collect(ctx)

		if err != nil {
			return nil, err
		}

		for _, dialog := range dialogs {
			peer := dialog.Entities
			id := Helper.GetInputPeerID(dialog.Peer)
			name := Helper.GetPeerName(id, peer) + " " + Helper.GetPeerType(id, peer)

			file := model.ObjThumb{
				Object: model.Object{
					ID:       strconv.FormatInt(id, 10),
					Name:     name,
					IsFolder: true,
					Modified: time.Unix(int64(dialog.Last.GetDate()), 0),
				},
			}
			files = append(files, &file)
		}

		// cache root result
		d.cache.Set("root", &files, cache.DefaultExpiration)
	} else { // list for chat
		if result, found := d.cache.Get(dirId); found {
			files, _ := result.(*[]model.Obj)
			return *files, nil
		}

		ids := strings.Split(dirId, ":")
		peerId := ids[0]

		peer, err := Helper.GetInputPeer(ctx, d.peersManager, peerId)
		if err != nil {
			return nil, err
		}

		if len(ids) == 1 {
			startDate := time.Date(2022, 10, 1, 0, 0, 0, 0, time.UTC)
			endDate := time.Date(2023, 1, 1, 0, 0, 0, 1, time.UTC)

			// Use a loop to iterate over the months between the start and end dates.
			for date := startDate; date.Before(endDate); date = date.AddDate(0, 1, 0) {
				// Format the date as a string in the "YYYY-MM" format.
				dateString := date.Format("2006-01")
				file := model.ObjThumb{
					Object: model.Object{
						ID:       peerId + ":" + dateString,
						Name:     dateString,
						IsFolder: true,
						Modified: date,
					},
				}
				files = append(files, &file)
			}

			d.cache.Set(dirId, &files, cache.DefaultExpiration)
		} else if len(ids) == 2 {
			dateString := ids[1]
			t, err := time.Parse("2006-01", dateString)
			if err != nil {
				return nil, err
			}

			dateStart := int(t.Unix())
			dateEnd := int(t.AddDate(0, 1, 0).Unix())
			iterator := query.Messages(d.client.API()).Search(peer.InputPeer()).MinDate(dateStart).MaxDate(dateEnd).Filter(&tg.InputMessagesFilterDocument{}).BatchSize(100).Iter()

			for iterator.Next(ctx) {
				message := iterator.Value()

				text := ""
				thumb := ""
				// thumbnailString := ""
				size := int64(0)
				switch message.Msg.(type) {
				case *tg.Message:
					text = message.Msg.(*tg.Message).GetMessage()
				case *tg.MessageService:
					text = message.Msg.String()
				}

				if doc, ok := message.Document(); ok {
					size = doc.Size
				} else if photo, ok := message.Photo(); ok {
					for _, thumb := range photo.Sizes {
						switch thumb.(type) {
						case (*tg.PhotoSizeProgressive):
							thumb := thumb.(*tg.PhotoSizeProgressive)
							size = int64(thumb.Sizes[len(thumb.Sizes)-1])
						}
					}
				}

				if mediaFile, ok := message.File(); ok {
					text = mediaFile.Name
					thumb = common.GetApiUrl(nil) + stdpath.Join("/p", args.ReqPath, text)
					thumb = utils.EncodePath(thumb, true)
					thumb += "?type=thumb&sign=" + sign.Sign(stdpath.Join(args.ReqPath, text))

					object := model.Object{
						ID:       peerId + ":" + dateString + ":" + strconv.Itoa(message.Msg.GetID()),
						Name:     text,
						IsFolder: false,
						Modified: time.Unix(int64(message.Msg.GetDate()), 0),
						Size:     size,
					}
					files = append(files, &model.ObjThumb{
						Object: object,
						Thumbnail: model.Thumbnail{
							Thumbnail: thumb,
						},
					})
				}
			}

			d.cache.Set(dirId, &files, cache.DefaultExpiration)
		}
	}

	return files, nil

}

func (d *Telegram) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	var link model.Link
	ids := strings.Split(file.GetID(), ":")

	if args.Type == "thumb" {
		peer, err := Helper.GetInputPeer(ctx, d.peersManager, ids[0])
		if err != nil {
			return nil, err
		}
		if result, ok := d.cache.Get("thumbnail:" + ids[2]); ok {
			data, _ := result.([]byte)

			link.Header = http.Header{}
			link.Header.Add("Content-Length", strconv.Itoa(len(data)))
			link.Header.Add("Content-Type", "image/jpeg")
			link.Header.Add("Content-Disposition", "filename=\""+ids[2]+".jpg\"")

			link.Data = io.NopCloser(bytes.NewReader(data))
			return &link, nil
		}

		messageId, _ := strconv.Atoi(ids[2])

		iterator := query.Messages(d.client.API()).Search(peer.InputPeer()).BatchSize(1).OffsetID(messageId + 1).Iter()
		if iterator.Next(ctx) {
			link = model.Link{}
			message := iterator.Value()

			thumbnailSize := 0
			thumbnailType := ""
			var location tg.InputFileLocationClass
			link.Header = http.Header{}
			if document, ok := message.Document(); ok {
				if photos, ok := document.GetThumbs(); ok {
					for _, size := range photos {
						if photoSize, ok := size.(*tg.PhotoSize); ok {
							thumbnailSize = photoSize.Size
							thumbnailType = photoSize.Type
						}
					}
				}
				fileLocation := document.AsInputDocumentFileLocation()
				fileLocation.ThumbSize = thumbnailType
				location = fileLocation
			}
			if photo, ok := message.Photo(); ok {
				if size, ok := photo.MapSizes().AsPhotoSize().First(); ok {
					thumbnailSize = size.Size
					thumbnailType = size.Type
				}

				location = &tg.InputPhotoFileLocation{
					FileReference: photo.FileReference,
					ID:            photo.ID,
					AccessHash:    photo.AccessHash,
					ThumbSize:     thumbnailType,
				}
			}

			if location != nil && thumbnailType != "" {
				link.Header.Add("Content-Length", strconv.Itoa(thumbnailSize))
				link.Header.Add("Content-Type", "image/jpeg")
				link.Header.Add("Content-Disposition", "filename=\""+ids[2]+".jpg\"")

				reader, writer := io.Pipe()
				link.Data = reader
				go func() {
					file, err := d.dcClient.UploadGetFile(ctx, &tg.UploadGetFileRequest{
						Location: location,
						Offset:   0,
						Limit:    1048576,
					})

					// downloader.NewDownloader().Download(d.dcClient, location).Stream(ctx, writer)
					// defer writer.Close()
					if err == nil {
						bytes := file.(*tg.UploadFile).GetBytes()
						writer.Write(bytes)
						d.cache.Add("thumbnail:"+ids[2], bytes[:thumbnailSize], time.Hour)
					}
					writer.Close()
				}()
				return &link, nil
			}
		}
		return nil, errs.ObjectNotFound
	}

	if len(ids) == 3 {
		peer, err := Helper.GetInputPeer(ctx, d.peersManager, ids[0])
		if err != nil {
			return nil, err
		}
		messageId, _ := strconv.Atoi(ids[2])

		iterator := query.Messages(d.client.API()).Search(peer.InputPeer()).BatchSize(1).OffsetID(messageId + 1).Iter()
		if iterator.Next(ctx) {
			link = model.Link{}
			message := iterator.Value()

			link.Header = http.Header{}
			if file, ok := message.File(); ok {
				link.Header.Add("Content-Type", file.MIMEType)
				link.Header.Add("Content-Disposition", "filename=\""+file.Name+"\"")

				reader, writer := io.Pipe()
				link.Data = reader
				go func() {
					_, err := downloader.NewDownloader().Download(d.dcClient, file.Location).Stream(ctx, writer)

					if err == nil {
						fmt.Print(err)
						writer.Close()
					}
				}()
				return &link, nil
			}
		}

		return nil, errs.ObjectNotFound
	}

	return nil, errs.NotImplement
}

func (d *Telegram) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	// TODO create folder
	return errs.NotImplement
}

func (d *Telegram) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	// TODO move obj
	return errs.NotImplement
}

func (d *Telegram) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	// TODO rename obj
	return errs.NotImplement
}

func (d *Telegram) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	// TODO copy obj
	return errs.NotImplement
}

func (d *Telegram) Remove(ctx context.Context, obj model.Obj) error {
	// TODO remove obj
	return errs.NotImplement
}

func (d *Telegram) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	// TODO upload file
	return errs.NotImplement
}

//func (d *Template) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

// LoadSession loads session from file.
func (d *Telegram) LoadSession(_ context.Context) ([]byte, error) {
	if d.Session != "" {
		return base64.StdEncoding.DecodeString(d.Session)
	}
	return nil, nil
}

// StoreSession stores session to file.
func (d *Telegram) StoreSession(_ context.Context, data []byte) error {
	d.Session = base64.StdEncoding.EncodeToString(data)
	op.MustSaveDriverStorage(d)
	return nil
}

var _ driver.Driver = (*Telegram)(nil)
