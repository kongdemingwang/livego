package rtmp

import (
	"errors"
	"github.com/livego/av"
	"github.com/livego/protocol/rtmp/cache"
	"github.com/livego/protocol/rtmp/rtmprelay"
	"github.com/orcaman/concurrent-map"
	"log"
	//"reflect"
	//"strings"
	"time"
)

var (
	EmptyID = ""
)

type RtmpStream struct {
	streams cmap.ConcurrentMap //key
}

func NewRtmpStream() *RtmpStream {
	ret := &RtmpStream{
		streams: cmap.New(),
	}
	go ret.CheckAlive()
	return ret
}

func (rs *RtmpStream) HandleReader(r av.ReadCloser) {
	info := r.Info()
	log.Printf("HandleReader: info[%v]", info)

	var stream *Stream
	i, ok := rs.streams.Get(info.Key)
	if stream, ok = i.(*Stream); ok {
		stream.TransStop()
		id := stream.ID()
		if id != EmptyID && id != info.UID {
			ns := NewStream()
			stream.Copy(ns)
			stream = ns
			rs.streams.Set(info.Key, ns)
		}
	} else {
		stream = NewStream()
		rs.streams.Set(info.Key, stream)
		stream.info = info
	}

	stream.AddReader(r)
}

func (rs *RtmpStream) HandleWriter(w av.WriteCloser) {
	info := w.Info()
	log.Printf("HandleWriter: info[%v]", info)

	var s *Stream
	ok := rs.streams.Has(info.Key)
	if !ok {
		s = NewStream()
		rs.streams.Set(info.Key, s)
		s.info = info
	} else {
		item, ok := rs.streams.Get(info.Key)
		if ok {
			s = item.(*Stream)
			s.AddWriter(w)
		}
	}
}

func (rs *RtmpStream) GetStreams() cmap.ConcurrentMap {
	return rs.streams
}

func (rs *RtmpStream) CheckAlive() {
	for {
		<-time.After(5 * time.Second)
		for item := range rs.streams.IterBuffered() {
			v := item.Val.(*Stream)
			if v.CheckAlive() == 0 {
				rs.streams.Remove(item.Key)
			}
		}
	}
}

type Stream struct {
	isStart bool
	cache   *cache.Cache
	r       av.ReadCloser
	ws      cmap.ConcurrentMap
	info    av.Info
}

type PackWriterCloser struct {
	init bool
	w    av.WriteCloser
}

func (p *PackWriterCloser) GetWriter() av.WriteCloser {
	return p.w
}

func NewStream() *Stream {
	return &Stream{
		cache: cache.NewCache(),
		ws:    cmap.New(),
	}
}

func (s *Stream) ID() string {
	if s.r != nil {
		return s.r.Info().UID
	}
	return EmptyID
}

func (s *Stream) GetReader() av.ReadCloser {
	return s.r
}

func (s *Stream) GetWs() cmap.ConcurrentMap {
	return s.ws
}

func (s *Stream) Copy(dst *Stream) {
	for item := range s.ws.IterBuffered() {
		v := item.Val.(*PackWriterCloser)
		s.ws.Remove(item.Key)
		v.w.CalcBaseTimestamp()
		dst.AddWriter(v.w)
	}
}

func (s *Stream) AddReader(r av.ReadCloser) {
	s.r = r
	go s.TransStart()
}

func (s *Stream) AddWriter(w av.WriteCloser) {
	info := w.Info()
	pw := &PackWriterCloser{w: w}
	s.ws.Set(info.UID, pw)
}

func (s *Stream) StartSubStaticPush() (ret bool) {
	ret = false
	_, masterPushObj := rtmprelay.GetStaticPushObjectbySubstream(s.info.URL)
	if masterPushObj != nil {
		err := masterPushObj.StartSubUrl(s.info.URL)
		if err == nil {
			ret = true
		}
	}
	return
}

func (s *Stream) StopSubStaticPush() {
	_, masterPushObj := rtmprelay.GetStaticPushObjectbySubstream(s.info.URL)
	if masterPushObj != nil {
		masterPushObj.StopSubUrl(s.info.URL)
	}
}

/*检测本application下是否配置static_push,
如果配置, 启动push远端的连接*/
func (s *Stream) StartStaticPush() (ret bool) {
	ret = false
	log.Printf("StartStaticPush: current url=%s", s.info.URL)
	pushurllist, err := rtmprelay.GetStaticPushList(s.info.URL)
	if err != nil || len(pushurllist) < 1 {
		log.Printf("StartStaticPush: GetStaticPushList error=%v", err)
		return
	}

	for _, pushurl := range pushurllist {
		//pushurl := pushurl + "/" + streamname
		log.Printf("StartStaticPush: static pushurl=%s", pushurl)

		staticpushObj := rtmprelay.GetAndCreateStaticPushObject(pushurl)
		if staticpushObj != nil {
			if err := staticpushObj.Start(); err != nil {
				log.Printf("StartStaticPush: staticpushObj.Start %s error=%v", pushurl, err)
			} else {
				log.Printf("StartStaticPush: staticpushObj.Start %s ok", pushurl)
				ret = true
			}
		} else {
			log.Printf("StartStaticPush GetStaticPushObject %s error", pushurl)
		}
	}

	return
}

func (s *Stream) StopStaticPush() {
	log.Printf("StopStaticPush: current url=%s", s.info.URL)
	pushurllist, err := rtmprelay.GetStaticPushList(s.info.URL)
	if err != nil || len(pushurllist) < 1 {
		log.Printf("StopStaticPush: GetStaticPushList error=%v", err)
		return
	}

	for _, pushurl := range pushurllist {
		//pushurl := pushurl + "/" + streamname
		log.Printf("StopStaticPush: static pushurl=%s", pushurl)

		staticpushObj, err := rtmprelay.GetStaticPushObject(pushurl)
		if (staticpushObj != nil) && (err == nil) {
			staticpushObj.Stop()
			rtmprelay.ReleaseStaticPushObject(pushurl)
			log.Printf("StopStaticPush: staticpushObj.Stop %s ", pushurl)
		} else {
			log.Printf("StopStaticPush GetStaticPushObject %s error", pushurl)
		}
	}
}

func (s *Stream) IsSubSendStaticPush() bool {
	ret := false
	_, masterPushObj := rtmprelay.GetStaticPushObjectbySubstream(s.info.URL)
	if masterPushObj != nil {
		ret = true
	}
	//log.Printf("IsSubSendStaticPush: %s, ret=%v", s.info.URL, ret)
	return ret
}

func (s *Stream) SendSubStaticPush(packet av.Packet) {
	index, masterPushObj := rtmprelay.GetStaticPushObjectbySubstream(s.info.URL)
	if masterPushObj == nil {
		return
	}

	packet.StreamIndex = uint32(index + 1)

	masterPushObj.WriteAvPacket(&packet)
}

func (s *Stream) IsSendStaticPush() bool {
	pushurllist, err := rtmprelay.GetStaticPushList(s.info.URL)
	if err != nil || len(pushurllist) < 1 {
		//log.Printf("SendStaticPush: GetStaticPushList error=%v", err)
		return false
	}

	for _, pushurl := range pushurllist {
		//pushurl := pushurl + "/" + streamname
		//log.Printf("SendStaticPush: static pushurl=%s", pushurl)

		staticpushObj, err := rtmprelay.GetStaticPushObject(pushurl)
		if (staticpushObj != nil) && (err == nil) {
			return true
			//staticpushObj.WriteAvPacket(&packet)
			//log.Printf("SendStaticPush: WriteAvPacket %s ", pushurl)
		} else {
			log.Printf("SendStaticPush GetStaticPushObject %s error", pushurl)
		}
	}
	return false
}

func (s *Stream) SendStaticPush(packet av.Packet) {
	pushurllist, err := rtmprelay.GetStaticPushList(s.info.URL)
	if err != nil || len(pushurllist) < 1 {
		return
	}

	for _, pushurl := range pushurllist {
		staticpushObj, err := rtmprelay.GetStaticPushObject(pushurl)
		if (staticpushObj != nil) && (err == nil) {
			staticpushObj.WriteAvPacket(&packet)
		} else {
			log.Printf("SendStaticPush GetStaticPushObject %s error", pushurl)
		}
	}
}

func (s *Stream) TransStart() {
	s.isStart = true
	var p av.Packet

	log.Printf("TransStart:%v", s.info)

	ret := s.StartStaticPush()

	if !ret {
		if s.IsSubSendStaticPush() {
			s.StartSubStaticPush()
		}
	}

	for {
		if !s.isStart {
			s.closeInter()
			return
		}
		err := s.r.Read(&p)
		if err != nil {
			s.closeInter()
			s.isStart = false
			return
		}

		if s.IsSendStaticPush() {
			s.SendStaticPush(p)
		} else if s.IsSubSendStaticPush() {
			s.SendSubStaticPush(p)
		}

		s.cache.Write(p)

		for item := range s.ws.IterBuffered() {
			v := item.Val.(*PackWriterCloser)
			if !v.init {
				//log.Printf("cache.send: %v", v.w.Info())
				if err = s.cache.Send(v.w); err != nil {
					log.Printf("[%s] send cache packet error: %v, remove", v.w.Info(), err)
					s.ws.Remove(item.Key)
					continue
				}
				v.init = true
			} else {
				new_packet := p
				//writeType := reflect.TypeOf(v.w)
				//log.Printf("w.Write: type=%v, %v", writeType, v.w.Info())
				if err = v.w.Write(&new_packet); err != nil {
					log.Printf("[%s] write packet error: %v, remove", v.w.Info(), err)
					s.ws.Remove(item.Key)
				}
			}
		}
	}
}

func (s *Stream) TransStop() {
	log.Printf("TransStop: %s", s.info.Key)

	if s.isStart && s.r != nil {
		s.r.Close(errors.New("stop old"))
	}

	s.isStart = false
}

func (s *Stream) CheckAlive() (n int) {
	if s.r != nil && s.isStart {
		if s.r.Alive() {
			n++
		} else {
			s.r.Close(errors.New("read timeout"))
		}
	}
	for item := range s.ws.IterBuffered() {
		v := item.Val.(*PackWriterCloser)
		if v.w != nil {
			if !v.w.Alive() && s.isStart {
				s.ws.Remove(item.Key)
				v.w.Close(errors.New("write timeout"))
				continue
			}
			n++
		}

	}
	return
}

func (s *Stream) closeInter() {
	if s.r != nil {
		s.StopStaticPush()
		s.StopSubStaticPush()
		log.Printf("[%v] publisher closed", s.r.Info())
	}

	for item := range s.ws.IterBuffered() {
		v := item.Val.(*PackWriterCloser)
		if v.w != nil {
			if v.w.Info().IsInterval() {
				v.w.Close(errors.New("closed"))
				s.ws.Remove(item.Key)
				log.Printf("[%v] player closed and remove\n", v.w.Info())
			}
		}

	}
}
