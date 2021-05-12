package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/awserr"
	"github.com/aws/aws-sdk-go-v2/aws/external"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func usage() {
	fmt.Fprintf(os.Stderr, `사용법: %s [options] {instance id|private IPv4 address|name}

Options:
  -v	        로그
  -p	        SSH key file 위치
  -l, --list    인스턴스 목록
  -c, --command 리모트 서버에 명령어 실행
`, filepath.Base(os.Args[0]))
	os.Exit(1)
}

var verboseFlag bool
var remoteCommand string
var listInstances bool
var kp string

var instIdRe = regexp.MustCompile(`i-[0-9a-fA-F]{8,17}$`)

type Instance struct {
	Name string
	Id   string
	Ip   string
	Ipp  string
}

type Instances []*Instance

func (s Instances) Len() int {
	return len(s)
}

func (s Instances) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s Instances) Less(i, j int) bool {
	switch strings.Compare(s[i].Name, s[j].Name) {
	case -1:
		return true
	case 1:
		return false
	}
	return s[i].Name > s[j].Name
}

func debugf(format string, args ...interface{}) {
	if verboseFlag {
		log.Printf(format, args...)
	}
}

func printError(err error) {
	if awsErr, ok := err.(awserr.Error); ok {
		log.Println("Error:", awsErr.Code(), awsErr.Message())
	} else {
		log.Println("Error:", err.Error())
	}
	os.Exit(1)
}

func reservationsToInstances(reservations []ec2.RunInstancesOutput) []*Instance {
	var instances []*Instance
	for _, reservation := range reservations {
		for _, instance := range reservation.Instances {
			name := "[None]"
			for _, keys := range instance.Tags {
				if *keys.Key == "Name" {
					name = url.QueryEscape(*keys.Value)
				}
			}

			publicIp := "None"
			if instance.PublicIpAddress != nil {
				publicIp = *instance.PublicIpAddress
			}
			instances = append(instances, &Instance{Name: name, Id: *instance.InstanceId, Ip: *instance.PrivateIpAddress, Ipp: publicIp})
		}
	}
	sort.Sort(Instances(instances))
	return instances
}

func printInstanceList(instances []*Instance) {
	if len(instances) == 0 {
		printError(errors.New("인스턴스가 없습니다."))
	} else {
		writer := tabwriter.NewWriter(os.Stdout, 4, 4, 4, ' ', tabwriter.TabIndent)
		fmt.Fprintln(writer, "Name\tInstance ID\tPrivate IP\tPublic IP")
		fmt.Fprintln(writer, "----\t-----------\t----------\t----------")
		for _, instance := range instances {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", instance.Name, instance.Id, instance.Ip, instance.Ipp)
		}
		writer.Flush()
	}
}

func fmtInstanceList(instances []*Instance) string {
	var buf bytes.Buffer
	writer := tabwriter.NewWriter(&buf, 4, 4, 4, ' ', tabwriter.TabIndent)
	fmt.Fprintln(writer, "n\tName\tInstance ID\tPrivate IP\tPublic IP")
	fmt.Fprintln(writer, "-\t----\t-----------\t----------\t----------")
	for i, instance := range instances {
		fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\n", i+1, instance.Name, instance.Id, instance.Ip, instance.Ipp)
	}
	writer.Flush()
	return buf.String()
}

func init() {
	p := os.Getenv("HOME") + "/.ssh/"
	if s := os.Getenv("AWS_KEY_PATH"); s != "" {
		p = s
	}

	flag.StringVar(&kp, "p", p, "SSH keys 위치입니다.  기본은 $HOME/.ssh 입니다.")
	flag.BoolVar(&verboseFlag, "v", false, "로그 찍기")

	flag.BoolVar(&listInstances, "l", false, "인스턴스 목록을 보여줍니다")
	flag.BoolVar(&listInstances, "list", false, "인스턴스 목록을 보여줍니다")

	flag.StringVar(&remoteCommand, "c", "", "리모트 서버에 명령어 실행")
	flag.StringVar(&remoteCommand, "command", "", "리모트 서버에 명령어 실행")
}

func findInstance(instance *Instance, reservations []ec2.RunInstancesOutput) (*ec2.Instance, error) {
	for _, reservation := range reservations {
		for _, ec2Instance := range reservation.Instances {
			if *ec2Instance.InstanceId == instance.Id {
				return &ec2Instance, nil
			}
		}
	}
	return nil, fmt.Errorf("인스턴스를 찾을 수 없습니다. %#v", instance)
}

func chooseInstance(lookup string, reservations []ec2.RunInstancesOutput) *ec2.Instance {
	var instanceList = reservationsToInstances(reservations)

	fmt.Printf(`Found more than one instance for '%s'.

Available instances:

%s

Which would you like to connect to? [1]
>>> `, lookup, fmtInstanceList(instanceList))
	var which string
	_, err := fmt.Scanln(&which)
	if err == io.EOF {
		fmt.Println("")
		os.Exit(0)
	}

	idx := 1
	if len(which) > 0 {
		idx, err = strconv.Atoi(which)
		if err != nil {
			printError(err)
		}
	}

	if idx < 1 || idx > len(instanceList) {
		printError(fmt.Errorf("Invalid index %d", idx))
	}

	instance, err := findInstance(instanceList[idx-1], reservations)
	if err != nil {
		printError(err)
	}

	return instance
}

func main() {
	log.SetFlags(0)
	log.SetPrefix(filepath.Base(os.Args[0]) + ": ")

	flag.Usage = usage
	flag.Parse()

	cfg, err := external.LoadDefaultAWSConfig()
	if err != nil {
		printError(err)
	}

	svc := ec2.New(cfg)

	var instanceStateFilter = ec2.Filter{
		Name: aws.String("instance-state-name"),
		Values: []string{
			"running",
			"pending",
		},
	}

	if flag.NArg() != 1 {
		if listInstances {
			debugf("aws api: describing instances")
			var params *ec2.DescribeInstancesInput

			params = &ec2.DescribeInstancesInput{
				Filters: []ec2.Filter{
					instanceStateFilter,
				},
			}

			req := svc.DescribeInstancesRequest(params)
			resp, err := req.Send()
			if err != nil {
				printError(err)
			}
			printInstanceList(reservationsToInstances(resp.Reservations))
			os.Exit(0)
		} else {
			flag.Usage()
		}
	}

	lookup := flag.Arg(0)
	var params *ec2.DescribeInstancesInput
	if ip := net.ParseIP(lookup); ip != nil {
		params = &ec2.DescribeInstancesInput{
			Filters: []ec2.Filter{
				{
					Name: aws.String("private-ip-address"),
					Values: []string{
						lookup,
					},
				},
				instanceStateFilter,
			},
		}
	} else if instIdRe.MatchString(lookup) {
		debugf("describing instance(s) by ID")
		params = &ec2.DescribeInstancesInput{
			InstanceIds: []string{lookup},
			Filters: []ec2.Filter{
				instanceStateFilter,
			},
		}
	} else {
		debugf("describing instance(s) by name")
		params = &ec2.DescribeInstancesInput{
			Filters: []ec2.Filter{
				{
					Name:   aws.String("tag:Name"),
					Values: []string{lookup},
				},
				instanceStateFilter,
			},
		}
	}

	debugf("aws api: describing instances")
	req := svc.DescribeInstancesRequest(params)
	resp, err := req.Send()
	if err != nil {
		printError(err)
	}

	debugf("aws api: got %d reservation(s)", len(resp.Reservations))

	var instance *ec2.Instance
	if len(resp.Reservations) == 0 {
		printError(fmt.Errorf("Found no instance '%s'", lookup))
	} else if len(resp.Reservations) == 1 {
		instance = &resp.Reservations[0].Instances[0]
	} else if len(resp.Reservations) > 1 {
		instance = chooseInstance(lookup, resp.Reservations)
	}

	binary, lookErr := exec.LookPath("ssh")
	if lookErr != nil {
		printError(lookErr)
	}

	args := []string{"-i", keypath(*instance.KeyName), "-l", "ec2-user", *instance.PublicIpAddress}
	if verboseFlag {
		args = append(args, "-v")
	}
	if len(remoteCommand) > 1 {
		args = append(args, remoteCommand)
	}

	cmd := exec.Command(binary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	debugf("running command %v", cmd.Args)
	if err := cmd.Run(); err != nil {
		printError(err)
	}
}

func keypath(s string) string {
	debugf("key path is: %s", kp)
	return path.Join(kp, s+".pem")
}
