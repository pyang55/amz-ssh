package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-sdk-go/aws"
	awsSession "github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	connect "github.com/aws/aws-sdk-go/service/ec2instanceconnect"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2" // imports as package "cli"

	sshutils "github.com/nodefortytwo/amz-ssh/pkg/sshutils"
)

var region = "eu-west-1"

func main() {
	// SetupSignalHandlers()

	app := &cli.App{
		Name:   "amz-ssh",
		Usage:  "connect to an ec2 instance via ec2 connect",
		Action: run,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "region",
				Aliases:     []string{"r"},
				Destination: &region,
				Value:       "eu-west-1",
			},
			&cli.StringFlag{
				Name:        "instance-id",
				Aliases:     []string{"i"},
				DefaultText: "instance id to ssh or tunnel through",
				Value:       "",
			},
			&cli.StringFlag{
				Name:        "user",
				Aliases:     []string{"u"},
				DefaultText: "os user of bastion",
				Value:       "ec2-user",
			},
			&cli.StringFlag{
				Name:        "tunnel",
				Aliases:     []string{"t"},
				DefaultText: "Host to tunnel to",
			},
			&cli.IntFlag{
				Name:        "port",
				Aliases:     []string{"p"},
				DefaultText: "local port to map to, defaults to tunnel port",
			},
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)

	}
}

func SetupSignalHandlers() {
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Println("\nGoodbye!")
		os.Exit(0)
	}()
}

func run(c *cli.Context) error {
	instanceID := c.String("instance-id")
	if instanceID == "" {
		log.Info("Looking for bastion spot request")
		siro, err := getSpotRequestByRole("bastion")
		if err != nil {
			return err
		}

		if len(siro.SpotInstanceRequests) > 0 {
			instanceID = aws.StringValue(siro.SpotInstanceRequests[0].InstanceId)
		} else {
			return errors.New("unable to find any valid bastion instances")
		}
	}
	log.Infof("Instance id: %s", instanceID)
	instanceOutput, err := getInstance(instanceID)
	if err != nil {
		return err
	}
	if len(instanceOutput.Reservations) == 0 || len(instanceOutput.Reservations[0].Instances) == 0 {
		return errors.New("instance not found")
	}

	instance := instanceOutput.Reservations[0].Instances[0]

	privateKey, publicKey, err := sshutils.GenerateKeys()
	if err != nil {
		return err
	}
	user := c.String("user")
	err = sendPublicKey(instance, user, publicKey)
	if err != nil {
		return err
	}

	bastionEndpoint := sshutils.NewEndpoint(aws.StringValue(instance.PublicIpAddress))
	bastionEndpoint.User = user
	bastionEndpoint.PrivateKey = privateKey

	if tunnel := sshutils.NewEndpoint(c.String("tunnel")); tunnel.Host != "" {
		p := c.Int("port")
		if p == 0 {
			p = tunnel.Port
		}
		return sshutils.Tunnel(p, tunnel, bastionEndpoint)
	}

	return sshutils.Connect(bastionEndpoint)
}

func sendPublicKey(instance *ec2.Instance, user, publicKey string) error {

	out, err := connectClient().SendSSHPublicKey(&connect.SendSSHPublicKeyInput{
		AvailabilityZone: instance.Placement.AvailabilityZone,
		InstanceId:       instance.InstanceId,
		InstanceOSUser:   aws.String(user),
		SSHPublicKey:     aws.String(publicKey),
	})
	if err != nil {
		return err
	}

	if *out.Success != true {
		return fmt.Errorf("request failed but no error was returned. Request ID: %s", aws.StringValue(out.RequestId))
	}

	return nil
}

func getInstance(id string) (*ec2.DescribeInstancesOutput, error) {
	return ec2Client().DescribeInstances(&ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(id)},
	})
}

func getSpotRequestByRole(role string) (*ec2.DescribeSpotInstanceRequestsOutput, error) {
	return ec2Client().DescribeSpotInstanceRequests(&ec2.DescribeSpotInstanceRequestsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:role"),
				Values: aws.StringSlice([]string{role}),
			},
			{
				Name:   aws.String("state"),
				Values: aws.StringSlice([]string{"active"}),
			},
			{
				Name:   aws.String("status-code"),
				Values: aws.StringSlice([]string{"fulfilled"}),
			},
		},
	})
}

func ec2Client() *ec2.EC2 {
	sess, err := awsSession.NewSession(&aws.Config{
		Region: aws.String(region),
	})
	if err != nil {
		log.Fatal(err)
	}

	return ec2.New(sess)
}

func connectClient() *connect.EC2InstanceConnect {
	sess, err := awsSession.NewSession(&aws.Config{
		Region: aws.String(region),
	})
	if err != nil {
		log.Fatal(err)
	}

	return connect.New(sess)
}
