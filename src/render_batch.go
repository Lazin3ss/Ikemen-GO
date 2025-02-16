//go:build !kinc && gl32

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"slices"
	"unsafe"

	"github.com/cespare/xxhash"
	mgl "github.com/go-gl/mathgl/mgl32"
)

var batchRenderer BatchRenderer

func init() {
	batchRenderer = NewBatchRenderer()
}

func NewBatchRenderer() BatchRenderer {
	return BatchRenderer{
		vertexBufferCache: make(map[uint64]uint32),
		palTexCache:       make(map[uint64]*Texture),
		state:             NewBatchRenderingState(),
	}
}

func NewBatchRenderingState() BatchRenderingState {
	return BatchRenderingState{
		paramList: make([]RenderUniformData, 0),
	}
}

type BatchRenderer struct {
	vertexBuffer2 uint32
	// Batch Render
	setInitialUniforms bool
	paletteTex         uint32
	fragUbo            uint32
	vertUbo            uint32
	indexUbo           uint32
	maxTextureUnits    int32
	curVertexBuffer    uint32
	vertexBufferCache  map[uint64]uint32
	lastUsedInBatch    RenderUniformData
	palTexCache        map[uint64]*Texture
	curLayer           int32
	curTexLayer        int32

	state BatchRenderingState
}
type FragmentUniforms struct {
	x1x2x4x3 [4]float32
	tint     [4]float32
	alpha    float32
	hue      float32
	gray     float32
	add      [3]float32
	mult     [3]float32
	mask     int32
	bitmask  int32
	isFlat   int32
	isRgba   int32
	isTrapez int32
	neg      int32
	padding  [4]byte
}

func (f *FragmentUniforms) Size() int {
	return 96
}

func (f *FragmentUniforms) String() string {
	str := fmt.Sprintf("x1x2x4x3: %v\n", f.x1x2x4x3)
	str = str + fmt.Sprintf("Tint: %v\n", f.tint)
	str = str + fmt.Sprintf("Add: %v\n", f.add)
	str = str + fmt.Sprintf("Alpha: %f\n", f.alpha)
	str = str + fmt.Sprintf("Mult: %v\n", f.mult)
	str = str + fmt.Sprintf("Gray: %f\n", f.gray)
	str = str + fmt.Sprintf("Mask: %d\n", f.mask)
	str = str + fmt.Sprintf("IsFlat: %d\n", f.isFlat)
	str = str + fmt.Sprintf("IsRgba: %d\n", f.isRgba)
	str = str + fmt.Sprintf("IsTropez: %d\n", f.isTrapez)
	str = str + fmt.Sprintf("Neg: %d\n", f.neg)
	str = str + fmt.Sprintf("Hue: %f\n", f.hue)
	return str
}
func (f *FragmentUniforms) createBitMask() int32 {
	return (f.isFlat << 0) | (f.isRgba << 1) | (f.isTrapez << 2) | (f.neg << 3)
}

func (f *FragmentUniforms) ToBytes() []byte {
	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.LittleEndian, f.x1x2x4x3[:])
	err = binary.Write(buf, binary.LittleEndian, f.tint[:])
	err = binary.Write(buf, binary.LittleEndian, f.alpha)
	err = binary.Write(buf, binary.LittleEndian, f.hue)
	err = binary.Write(buf, binary.LittleEndian, f.gray)
	err = binary.Write(buf, binary.LittleEndian, float32(0))
	err = binary.Write(buf, binary.LittleEndian, f.add[:])
	err = binary.Write(buf, binary.LittleEndian, float32(0))
	err = binary.Write(buf, binary.LittleEndian, f.mult[:])
	err = binary.Write(buf, binary.LittleEndian, float32(0))
	err = binary.Write(buf, binary.LittleEndian, f.mask)
	err = binary.Write(buf, binary.LittleEndian, f.createBitMask())
	err = binary.Write(buf, binary.LittleEndian, float32(0))
	err = binary.Write(buf, binary.LittleEndian, float32(0))

	if err != nil {
		log.Fatalf("binary.Write failed: %v", err)
	}
	return buf.Bytes()
}

type VertexUniforms struct {
	Modelview  mgl.Mat4
	Projection mgl.Mat4
}

func (v *VertexUniforms) ToBytes() []byte {
	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.LittleEndian, v.Modelview[:])
	err = binary.Write(buf, binary.LittleEndian, v.Projection[:])

	if err != nil {
		log.Fatalf("binary.Write failed: %v", err)
	}
	return buf.Bytes()
}

// -------- UBO
type IndexUniforms struct {
	FragUniformIndex   int32
	VertexUniformIndex int32
	PalLayer           int32
	TexLayer           int32
}

func (i *IndexUniforms) ToBytes() []byte {
	buf := new(bytes.Buffer)

	packedIndex := (i.VertexUniformIndex & 0x1F) |
		((i.FragUniformIndex & 0x3F) << 5) |
		((i.PalLayer & 0x1FF) << 11) |
		((i.TexLayer & 0x7F) << 20)

	err := binary.Write(buf, binary.LittleEndian, packedIndex)

	if err != nil {
		log.Fatalf("binary.Write failed: %v", err)
	}
	return buf.Bytes()
}

type RenderUniformData struct {
	window   [4]int32
	eq       BlendEquation // int
	src, dst BlendFunc     // int
	proj     mgl.Mat4
	tex      uint32
	paltex   uint32
	isRgba   int
	mask     int32
	isTropez int
	isFlat   int

	neg        int
	grayscale  float32
	hue        float32
	padd       [3]float32
	pmul       [3]float32
	tint       [4]float32
	alpha      float32
	modelView  mgl.Mat4
	trans      int32
	invblend   int32
	vertexData []float32
	// Possibly implement later
	//x1x2x4x3        [][]float32
	seqNo           int
	forSprite       bool
	UIMode          bool
	isTTF           bool
	ttf             *TtfFont
	palLayer        int32
	texLayer        int32
	depth           uint8
	vertexDataCache map[uint64]bool
	vertUniforms    VertexUniforms
	fragUniforms    FragmentUniforms
}

type BatchRenderGlobals struct {
	serializeBuffer         bytes.Buffer
	floatConvertBuffer      []byte
	vertexDataBuffer        [][]float32
	vertexDataBufferCounter int
	vertexCacheBuffer       []map[uint64]bool
}

type BatchRenderingState struct {
	paramList    []RenderUniformData
	batchGlobals BatchRenderGlobals
	curSDRSeqNo  int
	maxTextures  int
	uiMode       bool
}

func batchF32Encode(data []float32) []byte {
	batchRenderer.state.batchGlobals.floatConvertBuffer = batchRenderer.state.batchGlobals.floatConvertBuffer[:0]
	for _, f := range data {
		u := math.Float32bits(f)
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, u)
		batchRenderer.state.batchGlobals.floatConvertBuffer = append(batchRenderer.state.batchGlobals.floatConvertBuffer, b...)
	}
	return batchRenderer.state.batchGlobals.floatConvertBuffer
}

func BatchRender() {
	uniqueFrags := make([]FragmentUniforms, 0)
	uniqueVertexData := make([]VertexUniforms, 0)

	var currentBatch []RenderUniformData
	var lastHash uint64 = 0

	vertices := make([]float32, 0, 1024)

	// Aggregate all vertex data.
	for _, entry := range batchRenderer.state.paramList {
		vertices = append(vertices, entry.vertexData...)
	}

	if len(vertices) == 0 {
		return
	}

	// Set the global vertex data once.
	gfx.SetVertexData(vertices...)

	// 'first' tracks the starting vertex index of the current batch.
	var first int32 = 0
	// 'count' will now be calculated per batch within the loop.
	var count int32 = 0

	totalVertices := len(vertices) / 4
	indexUniforms := make([]IndexUniforms, 0, totalVertices)

	for i, entry := range batchRenderer.state.paramList {
		if i == 0 {
			uniqueFrags = append(uniqueFrags, entry.fragUniforms)
			uniqueVertexData = append(uniqueVertexData, entry.vertUniforms)
		} else {
			if !slices.Contains(uniqueFrags, entry.fragUniforms) {
				uniqueFrags = append(uniqueFrags, entry.fragUniforms)
			}
			if !slices.Contains(uniqueVertexData, entry.vertUniforms) {
				uniqueVertexData = append(uniqueVertexData, entry.vertUniforms)
			}
		}
	}
	batches := make([][]RenderUniformData, 0)

	for i, entry := range batchRenderer.state.paramList {
		data, _ := entry.Serialize()
		currentHash := xxhash.Sum64(data)

		// Start a new batch if the hash has changed and currentBatch is not empty
		if i == 0 || currentHash != lastHash {
			if len(currentBatch) > 0 {
				batches = append(batches, currentBatch)
				currentBatch = []RenderUniformData{}
			}
			lastHash = currentHash
		}
		currentBatch = append(currentBatch, entry)

		// Append the final batch after the last entry
		if i == len(batchRenderer.state.paramList)-1 && len(currentBatch) > 0 {
			batches = append(batches, currentBatch)
		}
	}
	batches = processBatches(batches)

	for _, batch := range batches {
		textures := getUniqueTextures(batch)
		for _, entry := range batch {
			numVertices := int32(len(entry.vertexData) / 4)
			var fragIndex int
			for j := 0; j < len(uniqueFrags); j++ {
				if uniqueFrags[j] == entry.fragUniforms {
					fragIndex = j
				}
			}
			var vertexIndex int
			for j := 0; j < len(uniqueVertexData); j++ {
				if uniqueVertexData[j] == entry.vertUniforms {
					vertexIndex = j
				}
			}
			var texIndex int
			for j := 0; j < len(textures); j++ {
				if textures[j] == entry.tex {
					texIndex = j
				}
			}

			indexUniform := IndexUniforms{}
			indexUniform.PalLayer = entry.palLayer
			indexUniform.TexLayer = int32(texIndex)
			indexUniform.FragUniformIndex = int32(fragIndex)
			indexUniform.VertexUniformIndex = int32(vertexIndex)
			for i := 0; i < int(numVertices); i++ {
				indexUniforms = append(indexUniforms, indexUniform)
			}
		}
	}

	gfx.UploadIndexUniformUBO(indexUniforms)
	gfx.UploadFragmentUBO(uniqueFrags)
	gfx.UploadVertexUBO(uniqueVertexData)
	gfx.BindUBOs()
	first = 0
	count = 0

	for _, batch := range batches {
		count = int32(getNumVertices(batch))
		if count > 0 || batch[0].isTTF {
			processBatchOptimized(batch, first, count)
		}
		first += count
	}

	// Reset state after processing.
	for i := 0; i < len(batchRenderer.state.batchGlobals.vertexDataBuffer); i++ {
		batchRenderer.state.batchGlobals.vertexDataBuffer[i] = batchRenderer.state.batchGlobals.vertexDataBuffer[i][:0]
		// Could be problem
		for _, buffer := range batchRenderer.state.batchGlobals.vertexCacheBuffer {
			for value := range buffer {
				delete(buffer, value)
			}
		}
	}
	batchRenderer.state.paramList = batchRenderer.state.paramList[:0]
	batchRenderer.state.batchGlobals.vertexDataBufferCounter = 0
	batchRenderer.state.curSDRSeqNo = 0
}

func processBatches(batches [][]RenderUniformData) [][]RenderUniformData {
	var finalBatches [][]RenderUniformData
	for _, batch := range batches {
		var subBatch []RenderUniformData
		uniqueTextures := make(map[uint32]bool)
		for _, entry := range batch {
			if _, exists := uniqueTextures[entry.tex]; !exists {
				if len(uniqueTextures) == int(batchRenderer.maxTextureUnits-1) {
					finalBatches = append(finalBatches, subBatch)
					subBatch = []RenderUniformData{}
					uniqueTextures = make(map[uint32]bool)
				}
				uniqueTextures[entry.tex] = true
			}
			subBatch = append(subBatch, entry)
		}
		if len(subBatch) > 0 {
			finalBatches = append(finalBatches, subBatch)
		}
	}
	return finalBatches
}

func getNumVertices(batch []RenderUniformData) int {
	totalVertices := 0
	for _, entry := range batch {
		totalVertices += len(entry.vertexData)
	}
	return totalVertices / 4
}

func getUniqueTextures(batch []RenderUniformData) []uint32 {
	uniqueTextures := make([]uint32, 0, 15)
	for i := 0; i < len(batch); i++ {
		if !slices.Contains(uniqueTextures, batch[i].tex) {
			uniqueTextures = append(uniqueTextures, batch[i].tex)
		}
	}
	return uniqueTextures
}

func processBatchOptimized(batch []RenderUniformData, start int32, total int32) {
	if len(batch) == 0 {
		return
	}
	uniqueTextures := make([]uint32, 0, 15)
	for i := 0; i < len(batch); i++ {
		if !slices.Contains(uniqueTextures, batch[i].tex) {
			uniqueTextures = append(uniqueTextures, batch[i].tex)
		}
	}
	//var indices []int32

	srd := batch[0]
	if srd.isTTF {
		(*srd.ttf).PrintBatch()
		return
	}

	//UIMode = srd.UIMode
	gfx.Scissor(srd.window[0], srd.window[1], srd.window[2], srd.window[3])
	gfx.SetPipeline(srd.eq, srd.src, srd.dst)

	var names []string
	if srd.forSprite {
		gfx.SetTextureArrayWithHandle("palArray", batchRenderer.paletteTex)
		names = make([]string, len(uniqueTextures))
		for i, h := range uniqueTextures {
			str := fmt.Sprintf("tex[%d]", i)
			names[i] = str
			gfx.SetTextureWithHandle(str, h)
		}
	}

	gfx.RenderQuadBatchAtIndex(start, total)

	if srd.forSprite && len(uniqueTextures) > 0 {
		gfx.UnbindTextures(names)
	}
	gfx.ReleasePipeline()

	if srd.forSprite {
		gfx.DisableScissor()
	}
}

func (r *RenderUniformData) Serialize() ([]byte, error) {
	buf := &batchRenderer.state.batchGlobals.serializeBuffer
	buf.Reset()

	if err := binary.Write(buf, binary.LittleEndian, r.window[:]); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, int32(r.eq)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, int32(r.src)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, int32(r.dst)); err != nil {
		return nil, err
	}

	if err := binary.Write(buf, binary.LittleEndian, r.isTTF); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func NewRenderUniformData() RenderUniformData {
	rud := RenderUniformData{}
	if len(batchRenderer.state.batchGlobals.vertexDataBuffer) == 0 {
		batchRenderer.state.batchGlobals.vertexDataBuffer = make([][]float32, 256)

		for i := 0; i < 256; i++ {
			batchRenderer.state.batchGlobals.vertexDataBuffer[i] = make([]float32, 0, 24)
		}
	}
	if len(batchRenderer.state.batchGlobals.vertexCacheBuffer) == 0 {
		batchRenderer.state.batchGlobals.vertexCacheBuffer = make([]map[uint64]bool, 256)

		for i := 0; i < 256; i++ {
			batchRenderer.state.batchGlobals.vertexCacheBuffer[i] = make(map[uint64]bool)
		}
	}

	if len(batchRenderer.state.batchGlobals.vertexDataBuffer) > batchRenderer.state.batchGlobals.vertexDataBufferCounter {
		rud.vertexData = batchRenderer.state.batchGlobals.vertexDataBuffer[batchRenderer.state.batchGlobals.vertexDataBufferCounter]
		rud.vertexDataCache = batchRenderer.state.batchGlobals.vertexCacheBuffer[batchRenderer.state.batchGlobals.vertexDataBufferCounter]
		batchRenderer.state.batchGlobals.vertexDataBufferCounter++
	} else {
		batchRenderer.state.batchGlobals.vertexDataBuffer = append(batchRenderer.state.batchGlobals.vertexDataBuffer, make([]float32, 0, 24))
		rud.vertexData = batchRenderer.state.batchGlobals.vertexDataBuffer[batchRenderer.state.batchGlobals.vertexDataBufferCounter]
		batchRenderer.state.batchGlobals.vertexCacheBuffer = append(batchRenderer.state.batchGlobals.vertexCacheBuffer, make(map[uint64]bool))
		rud.vertexDataCache = batchRenderer.state.batchGlobals.vertexCacheBuffer[batchRenderer.state.batchGlobals.vertexDataBufferCounter]
		batchRenderer.state.batchGlobals.vertexDataBufferCounter++
	}
	return rud
}

func CalculateRenderData(rp RenderParams) {
	if !rp.IsValid() {
		return
	}

	rmInitSub(&rp)

	rd := NewRenderUniformData()
	rd.forSprite = true
	rd.UIMode = batchRenderer.state.uiMode

	neg, grayscale, padd, pmul, invblend, hue := false, float32(0), [3]float32{0, 0, 0}, [3]float32{1, 1, 1}, int32(0), float32(0)
	tint := [4]float32{float32(rp.tint&0xff) / 255, float32(rp.tint>>8&0xff) / 255,
		float32(rp.tint>>16&0xff) / 255, float32(rp.tint>>24&0xff) / 255}

	if rp.pfx != nil {
		blending := rp.trans
		//if rp.trans == -2 || rp.trans == -1 || (rp.trans&0xff > 0 && rp.trans>>10&0xff >= 255) {
		//	blending = true
		//}
		neg, grayscale, padd, pmul, invblend, hue = rp.pfx.getFcPalFx(false, int(blending))
		//if rp.trans == -2 && invblend < 1 {
		//padd[0], padd[1], padd[2] = -padd[0], -padd[1], -padd[2]
		//}
	}

	proj := mgl.Ortho(0, float32(sys.scrrect[2]), 0, float32(sys.scrrect[3]), -65535, 65535)
	modelview := mgl.Translate3D(0, float32(sys.scrrect[3]), 0)
	rd.window = *rp.window

	// gfx.Scissor(rp.window[0], rp.window[1], rp.window[2], rp.window[3])
	renderWithBlending(func(eq BlendEquation, src, dst BlendFunc, a float32) {
		rmTileSubBatch(modelview, rp, &rd)
		rd.tex = rp.tex.handle
		rd.texLayer = rp.tex.layer
		rd.eq = eq
		rd.src = src
		rd.dst = dst
		rd.proj = proj
		rd.tex = rp.tex.handle
		if rp.paltex == nil {
			rd.isRgba = 1
		} else {
			//rd.paltex = rp.paltex.handle
			rd.palLayer = rp.paltex.layer
			rd.isRgba = 0
		}
		rd.mask = rp.mask
		rd.isTropez = int(Btoi(AbsF(AbsF(rp.xts)-AbsF(rp.xbs)) > 0.001))
		rd.isFlat = 0
		rd.neg = int(Btoi(neg))
		rd.grayscale = grayscale
		rd.hue = hue
		rd.padd = padd
		rd.pmul = pmul
		rd.tint = tint
		rd.alpha = a
		//rd.modelView = modelview
		//rd.trans = rp.trans
		//rd.invblend = invblend
		if rp.paltex == nil {
			rd.fragUniforms.isRgba = 1
		} else {
			//rd.paltex = rp.paltex.handle
			rd.palLayer = rp.paltex.layer
			rd.fragUniforms.isRgba = 0
		}
		rd.fragUniforms.mask = rp.mask
		rd.fragUniforms.isTrapez = int32(Btoi(AbsF(AbsF(rp.xts)-AbsF(rp.xbs)) > 0.001))
		rd.fragUniforms.isFlat = 0
		rd.fragUniforms.neg = int32(Btoi(neg))
		rd.fragUniforms.gray = grayscale
		rd.fragUniforms.hue = hue
		rd.fragUniforms.add = padd
		rd.fragUniforms.mult = pmul
		rd.fragUniforms.tint = tint
		rd.fragUniforms.alpha = a

		rd.vertUniforms.Projection = proj
		BatchParam(&rd)
		rd.seqNo = batchRenderer.state.curSDRSeqNo
		batchRenderer.state.curSDRSeqNo++
		// fmt.Printf("In Prerender: eq: %d src %d dst %d a %f seqNo: %d \n", eq, src, dst, a, rd.seqNo)

	}, rp.trans, rp.paltex != nil, invblend, &neg, &padd, &pmul, rp.paltex == nil)
}

func BatchParam(rp *RenderUniformData) {
	if rp != nil {
		batchRenderer.state.paramList = append(batchRenderer.state.paramList, *rp)
	}
}

func (r *RenderUniformData) AppendVertexData(vertices []float32) {
	data := batchF32Encode(vertices)

	hash := xxhash.Sum64(data)
	if _, ok := r.vertexDataCache[hash]; !ok {
		r.vertexData = append(r.vertexData, vertices...)
		r.vertexDataCache[hash] = true
	}
}

// Render a quad with optional horizontal tiling
func rmTileHSubBatch(rd *RenderUniformData, modelview mgl.Mat4, x1, y1, x2, y2, x3, y3, x4, y4, width float32,
	tl Tiling, rcx float32) {
	//            p3
	//    p4 o-----o-----o- - -o
	//      /      |      \     ` .
	//     /       |       \       `.
	//    o--------o--------o- - - - o
	//   p1         p2
	topdist := (x3 - x4) * (1 + float32(tl.xspacing)/width)
	botdist := (x2 - x1) * (1 + float32(tl.xspacing)/width)
	if AbsF(topdist) >= 0.01 {
		db := (x4 - rcx) * (botdist - topdist) / AbsF(topdist)
		x1 += db
		x2 += db
	}

	// Compute left/right tiling bounds (or right/left when topdist < 0)
	xmax := float32(sys.scrrect[2])
	left, right := int32(0), int32(1)
	if topdist >= 0.01 {
		left = 1 - int32(math.Ceil(float64(MaxF(x3/topdist, x2/botdist))))
		right = int32(math.Ceil(float64(MaxF((xmax-x4)/topdist, (xmax-x1)/botdist))))
	} else if topdist <= -0.01 {
		left = 1 - int32(math.Ceil(float64(MaxF((xmax-x3)/-topdist, (xmax-x2)/-botdist))))
		right = int32(math.Ceil(float64(MaxF(x4/-topdist, x1/-botdist))))
	}

	if tl.xflag != 1 {
		left = 0
		right = Min(right, Max(tl.xflag, 1))
	}

	buffer := make([]float32, 0)
	xs := make([][]float32, 0)
	// Draw all quads in one loop
	for n := left; n < right; n++ {
		x1d, x2d := x1+float32(n)*botdist, x2+float32(n)*botdist
		x3d, x4d := x3+float32(n)*topdist, x4+float32(n)*topdist
		buffer = append(buffer, []float32{
			x2d, y2, 1, 1,
			x3d, y3, 1, 0,
			x1d, y1, 0, 1,

			x1d, y1, 0, 1,
			x3d, y3, 1, 0,
			x4d, y4, 0, 0,
		}...)
		xs = append(xs, []float32{x1d, x2d, x4d, x3d})
	}
	if len(buffer) > 0 {
		if rd == nil {
			gfx.SetVertexData(buffer...)
			vertex := int32(0)
			for i := 0; i < len(xs); i++ {
				gfx.SetUniformMatrix("modelview", modelview[:])
				gfx.SetUniformF("x1x2x4x3", xs[i][0], xs[i][1], xs[i][2], xs[i][3]) // this uniform is optional
				gfx.RenderQuadAtIndex(vertex)
				vertex += 4
			}
		} else {
			rd.modelView = modelview
			rd.vertUniforms.Modelview = modelview
			rd.AppendVertexData(buffer)
		}
	}
}

func rmTileSubBatch(modelview mgl.Mat4, rp RenderParams, rd *RenderUniformData) {
	x1, y1 := rp.x+rp.rxadd*rp.ys*float32(rp.size[1]), rp.rcy+((rp.y-rp.ys*float32(rp.size[1]))-rp.rcy)*rp.vs
	x2, y2 := x1+rp.xbs*float32(rp.size[0]), y1
	x3, y3 := rp.x+rp.xts*float32(rp.size[0]), rp.rcy+(rp.y-rp.rcy)*rp.vs
	x4, y4 := rp.x, y3
	//var pers float32
	//if AbsF(rp.xts) < AbsF(rp.xbs) {
	//	pers = AbsF(rp.xts) / AbsF(rp.xbs)
	//} else {
	//	pers = AbsF(rp.xbs) / AbsF(rp.xts)
	//}
	if !rp.rot.IsZero() {
		//	kaiten(&x1, &y1, float64(agl), rcx, rcy, vs)
		//	kaiten(&x2, &y2, float64(agl), rcx, rcy, vs)
		//	kaiten(&x3, &y3, float64(agl), rcx, rcy, vs)
		//	kaiten(&x4, &y4, float64(agl), rcx, rcy, vs)
		if rp.vs != 1 {
			y1 = rp.rcy + ((rp.y - rp.ys*float32(rp.size[1])) - rp.rcy)
			y2 = y1
			y3 = rp.rcy + (rp.y - rp.rcy)
			y4 = y3
		}
		if rp.projectionMode == 0 {
			modelview = modelview.Mul4(mgl.Translate3D(rp.rcx, rp.rcy, 0))
		} else if rp.projectionMode == 1 {
			//This is the inverse of the orthographic projection matrix
			matrix := mgl.Mat4{float32(sys.scrrect[2] / 2.0), 0, 0, 0, 0, float32(sys.scrrect[3] / 2), 0, 0, 0, 0, -65535, 0, float32(sys.scrrect[2] / 2), float32(sys.scrrect[3] / 2), 0, 1}
			modelview = modelview.Mul4(mgl.Translate3D(0, -float32(sys.scrrect[3]), rp.fLength))
			modelview = modelview.Mul4(matrix)
			modelview = modelview.Mul4(mgl.Frustum(-float32(sys.scrrect[2])/2/rp.fLength, float32(sys.scrrect[2])/2/rp.fLength, -float32(sys.scrrect[3])/2/rp.fLength, float32(sys.scrrect[3])/2/rp.fLength, 1.0, 65535))
			modelview = modelview.Mul4(mgl.Translate3D(-float32(sys.scrrect[2])/2.0, float32(sys.scrrect[3])/2.0, -rp.fLength))
			modelview = modelview.Mul4(mgl.Translate3D(rp.rcx, rp.rcy, 0))
		} else if rp.projectionMode == 2 {
			matrix := mgl.Mat4{float32(sys.scrrect[2] / 2.0), 0, 0, 0, 0, float32(sys.scrrect[3] / 2), 0, 0, 0, 0, -65535, 0, float32(sys.scrrect[2] / 2), float32(sys.scrrect[3] / 2), 0, 1}
			//modelview = modelview.Mul4(mgl.Translate3D(0, -float32(sys.scrrect[3]), 2048))
			modelview = modelview.Mul4(mgl.Translate3D(rp.rcx-float32(sys.scrrect[2])/2.0-rp.xOffset, rp.rcy-float32(sys.scrrect[3])/2.0+rp.yOffset, rp.fLength))
			modelview = modelview.Mul4(matrix)
			modelview = modelview.Mul4(mgl.Frustum(-float32(sys.scrrect[2])/2/rp.fLength, float32(sys.scrrect[2])/2/rp.fLength, -float32(sys.scrrect[3])/2/rp.fLength, float32(sys.scrrect[3])/2/rp.fLength, 1.0, 65535))
			modelview = modelview.Mul4(mgl.Translate3D(rp.xOffset, -rp.yOffset, -rp.fLength))
		}

		modelview = modelview.Mul4(mgl.Scale3D(1, rp.vs, 1))
		modelview = modelview.Mul4(
			mgl.Rotate3DX(-rp.rot.xangle * math.Pi / 180.0).Mul3(
				mgl.Rotate3DY(rp.rot.yangle * math.Pi / 180.0)).Mul3(
				mgl.Rotate3DZ(rp.rot.angle * math.Pi / 180.0)).Mat4())
		modelview = modelview.Mul4(mgl.Translate3D(-rp.rcx, -rp.rcy, 0))
		drawQuadsBatch(rd, modelview, x1, y1, x2, y2, x3, y3, x4, y4)
		return
	}

	if rp.tile.yflag == 1 && rp.xbs != 0 {
		x1d, y1d, x2d, y2d, x3d, y3d, x4d, y4d := x1, y1, x2, y2, x3, y3, x4, y4
		for {
			x1d, y1d = x4d, y4d+rp.ys*rp.vs*float32(rp.tile.yspacing)
			x2d, y2d = x3d, y1d
			x3d = x4d - rp.rxadd*rp.ys*float32(rp.size[1]) + (rp.xts/rp.xbs)*(x3d-x4d)
			y3d = y2d + rp.ys*rp.vs*float32(rp.size[1])
			x4d = x4d - rp.rxadd*rp.ys*float32(rp.size[1])
			if AbsF(y3d-y4d) < 0.01 {
				break
			}
			y4d = y3d
			if rp.ys*(float32(rp.size[1])+float32(rp.tile.yspacing)) < 0 {
				if y1d <= float32(-sys.scrrect[3]) && y4d <= float32(-sys.scrrect[3]) {
					break
				}
			} else if y1d >= 0 && y4d >= 0 {
				break
			}
			if (0 > y1d || 0 > y4d) &&
				(y1d > float32(-sys.scrrect[3]) || y4d > float32(-sys.scrrect[3])) {
				rmTileHSubBatch(rd, modelview, x1d, y1d, x2d, y2d, x3d, y3d, x4d, y4d,
					float32(rp.size[0]), rp.tile, rp.rcx)
			}
		}
	}
	if rp.tile.yflag == 0 || rp.xts != 0 {
		n := rp.tile.yflag
		for {
			if rp.ys*(float32(rp.size[1])+float32(rp.tile.yspacing)) > 0 {
				if y1 <= float32(-sys.scrrect[3]) && y4 <= float32(-sys.scrrect[3]) {
					break
				}
			} else if y1 >= 0 && y4 >= 0 {
				break
			}
			if (0 > y1 || 0 > y4) &&
				(y1 > float32(-sys.scrrect[3]) || y4 > float32(-sys.scrrect[3])) {
				rmTileHSubBatch(rd, modelview, x1, y1, x2, y2, x3, y3, x4, y4,
					float32(rp.size[0]), rp.tile, rp.rcx)
			}
			if rp.tile.yflag != 1 && n != 0 {
				n--
			}
			if n == 0 {
				break
			}
			x4, y4 = x1, y1-rp.ys*rp.vs*float32(rp.tile.yspacing)
			x3, y3 = x2, y4
			x2 = x1 + rp.rxadd*rp.ys*float32(rp.size[1]) + (rp.xbs/rp.xts)*(x2-x1)
			y2 = y3 - rp.ys*rp.vs*float32(rp.size[1])
			x1 = x1 + rp.rxadd*rp.ys*float32(rp.size[1])
			if AbsF(y1-y2) < 0.01 {
				break
			}
			y1 = y2
		}
	}
}

func drawQuadsBatch(rd *RenderUniformData, modelview mgl.Mat4, x1, y1, x2, y2, x3, y3, x4, y4 float32) {
	if rd == nil {
		gfx.SetUniformMatrix("modelview", modelview[:])
		gfx.SetUniformF("x1x2x4x3", x1, x2, x4, x3) // this uniform is optional
		gfx.SetVertexData(
			x2, y2, 1, 1,
			x3, y3, 1, 0,
			x1, y1, 0, 1,
			x4, y4, 0, 0)

		gfx.RenderQuad()
	} else {
		rd.AppendVertexData([]float32{
			x2, y2, 1, 1,
			x3, y3, 1, 0,
			x1, y1, 0, 1,

			x1, y1, 0, 1,
			x3, y3, 1, 0,
			x4, y4, 0, 0,
		})
		rd.modelView = modelview
		rd.vertUniforms.Modelview = modelview
	}
}

func CalculateRectData(rect [4]int32, color uint32, trans int32) {
	rd := NewRenderUniformData()
	rd.UIMode = batchRenderer.state.uiMode

	r := float32(color>>16&0xff) / 255
	g := float32(color>>8&0xff) / 255
	b := float32(color&0xff) / 255

	modelview := mgl.Translate3D(0, float32(sys.scrrect[3]), 0)
	proj := mgl.Ortho(0, float32(sys.scrrect[2]), 0, float32(sys.scrrect[3]), -65535, 65535)

	x1, y1 := float32(rect[0]), -float32(rect[1])
	x2, y2 := float32(rect[0]+rect[2]), -float32(rect[1]+rect[3])

	renderWithBlending(func(eq BlendEquation, src, dst BlendFunc, a float32) {

		rd.eq = eq
		rd.src = src
		rd.dst = dst
		// rd.vertexData = append(rd.vertexData, []float32{
		// 	x1, y2, 0, 1,
		// 	x1, y1, 0, 0,
		// 	x2, y1, 1, 0,

		// 	x1, y2, 0, 1,
		// 	x2, y1, 1, 0,
		// 	x2, y2, 1, 1,
		// }...)
		rd.AppendVertexData([]float32{
			x1, y2, 0, 1,
			x1, y1, 0, 0,
			x2, y1, 1, 0,

			x1, y2, 0, 1,
			x2, y1, 1, 0,
			x2, y2, 1, 1,
		})
		rd.modelView = modelview
		rd.proj = proj
		rd.isFlat = 1
		rd.tint = [4]float32{r, g, b, a}
		rd.trans = trans
		rd.invblend = 0

		rd.fragUniforms.isFlat = 1
		rd.fragUniforms.tint = [4]float32{r, g, b, a}
		rd.vertUniforms.Projection = proj
		rd.vertUniforms.Modelview = modelview

		BatchParam(&rd)
		rd.seqNo = batchRenderer.state.curSDRSeqNo

		batchRenderer.state.curSDRSeqNo++
	}, trans, true, 0, nil, nil, nil, false)
}

func PaletteToTextureSub(pal []uint32) *Texture {
	//tx := newTexture(256, 1, 32, false)
	//tx.SetData(unsafe.Slice((*byte)(unsafe.Pointer(&pal[0])), len(pal)*4))
	tx := newTextureLayer(256, 1, 32, batchRenderer.curLayer, false)
	tx.SetDataArray(batchRenderer.curLayer, batchRenderer.paletteTex, unsafe.Slice((*byte)(unsafe.Pointer(&pal[0])), len(pal)*4))
	batchRenderer.curLayer++
	return tx
}

/* Do not be afraid of this */
func PaletteToTextureBatch(pal []uint32) *Texture {
	return GenerateTextureFromPalette(pal)
}

func GenerateTextureFromPalette(pal []uint32) *Texture {
	key := HashPalette(pal)

	if texture, exists := batchRenderer.palTexCache[key]; exists {
		return texture
	}

	newTexture := PaletteToTextureSub(pal)
	batchRenderer.palTexCache[key] = newTexture
	return newTexture
}

func uint32SliceToBytes(slice []uint32) []byte {
	var buf bytes.Buffer
	for _, val := range slice {
		if err := binary.Write(&buf, binary.LittleEndian, val); err != nil {
			return nil
		}
	}
	return buf.Bytes()
}

func HashPalette(pal []uint32) uint64 {
	return xxhash.Sum64(uint32SliceToBytes(pal))
}
