package converter

import (
	"fmt"
	"io"
	"io/ioutil"
	"path"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/protoc-gen-go/descriptor"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
	"github.com/sirupsen/logrus"
)

const (
	openAPISpecFileName = "spec.json"
)

// Converter is everything you need to convert Micro protos into an OpenAPI spec:
type Converter struct {
	componentSchemas map[string]*openapi3.Schema
	logger           *logrus.Logger
	openAPISpec      *openapi3.Swagger
	sourceInfo       *sourceCodeInfo
}

// New returns a configured converter:
func New(logger *logrus.Logger) *Converter {
	return &Converter{
		componentSchemas: make(map[string]*openapi3.Schema),
		logger:           logger,
	}
}

// ConvertFrom tells the convert to work on the given input:
func (c *Converter) ConvertFrom(rd io.Reader) (*plugin.CodeGeneratorResponse, error) {
	c.logger.Debug("Reading code generation request")
	input, err := ioutil.ReadAll(rd)
	if err != nil {
		c.logger.Errorf("Failed to read request: %v", err)
		return nil, err
	}

	req := &plugin.CodeGeneratorRequest{}
	err = proto.Unmarshal(input, req)
	if err != nil {
		c.logger.Errorf("Can't unmarshal input: %v", err)
		return nil, err
	}

	c.openAPISpec = &openapi3.Swagger{
		Components: openapi3.Components{
			Schemas: make(map[string]*openapi3.SchemaRef),
		},
		Info: &openapi3.Info{
			Title:       "Micro API",
			Description: "Generated by protoc-gen-openapi",
			Version:     "1",
		},
		OpenAPI: "3.0.0",
		Paths:   make(openapi3.Paths),
	}
	c.openAPISpec.AddServer(
		&openapi3.Server{
			URL:         "https://cruft.micro.com",
			Description: "Micro API",
		},
	)

	c.logger.Debugf("Converting input: %v", err)
	return c.convert(req)
}

// Converts a proto file into an OpenAPI spec:
func (c *Converter) convertFile(file *descriptor.FileDescriptorProto) error {

	// Input filename:
	protoFileName := path.Base(file.GetName())

	// Otherwise process MESSAGES (packages):
	pkg, ok := c.relativelyLookupPackage(globalPkg, file.GetPackage())
	if !ok {
		return fmt.Errorf("no such package found: %s", file.GetPackage())
	}

	// Process messages:
	for _, msg := range file.GetMessageType() {

		// Convert the message:
		c.logger.Infof("Generating component schema for message (%s) from proto file (%s)", msg.GetName(), protoFileName)
		componentSchema, err := c.convertMessageType(pkg, msg)
		if err != nil {
			c.logger.Errorf("Failed to convert (%s): %v", protoFileName, err)
			return err
		}
		c.componentSchemas[componentSchema.Title] = componentSchema
		// c.openAPISpec.Components.Schemas[componentSchema.Title] = componentSchema.NewRef()
	}

	// spew.Fdump(os.Stderr, c.componentSchemas)

	// Process services:
	for _, svc := range file.GetService() {

		// Convert the service:
		c.logger.Infof("Generating service (%s) from proto file (%s)", svc.GetName(), protoFileName)
		servicePaths, err := c.convertServiceType(file, pkg, svc)
		if err != nil {
			c.logger.Errorf("Failed to convert (%s): %v", protoFileName, err)
			return err
		}

		// Add the paths to our API:
		for path, pathItem := range servicePaths {
			c.openAPISpec.Paths[path] = pathItem
		}
	}

	return nil
}

func (c *Converter) convert(req *plugin.CodeGeneratorRequest) (*plugin.CodeGeneratorResponse, error) {

	c.sourceInfo = newSourceCodeInfo(req.GetProtoFile())

	generateTargets := make(map[string]bool)
	for _, file := range req.GetFileToGenerate() {
		generateTargets[file] = true
	}

	res := &plugin.CodeGeneratorResponse{}
	for _, file := range req.GetProtoFile() {
		if file.GetPackage() == "" {
			c.logger.Warnf("Proto file (%s) doesn't specify a package", file.GetName())
			continue
		}

		for _, msg := range file.GetMessageType() {
			c.logger.Debugf("Loading a message (%s/%s)", file.GetPackage(), msg.GetName())
			c.registerType(file.Package, msg)
		}

		if _, ok := generateTargets[file.GetName()]; ok {
			c.logger.Debugf("Converting file (%s)", file.GetName())
			if err := c.convertFile(file); err != nil {
				res.Error = proto.String(fmt.Sprintf("Failed to convert %s: %v", file.GetName(), err))
				return res, err
			}
		}
	}

	// Marshal the OpenAPI spec:
	marshaledSpec, err := c.openAPISpec.MarshalJSON()
	if err != nil {
		c.logger.Errorf("Unable to marshal the OpenAPI spec: %v", err)
		return nil, err
	}

	// Add a response file:
	res.File = []*plugin.CodeGeneratorResponse_File{
		{
			Name:    proto.String(openAPISpecFileName),
			Content: proto.String(string(marshaledSpec)),
		},
	}

	return res, nil
}
