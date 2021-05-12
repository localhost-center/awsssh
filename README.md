# awssh
Golang으로 만든 ssh 터미널 접속 및 터널링 툴

## 사용법

### 접속

```
# by AWS instance ID
$ awssh i-0017c8b3

# by the instance Name tag
$ awssh api-server

# by the instance private IP address
$ awssh 1.2.3.4
```

### Other options/flags

"default" 외 AWS 프로필 지정:

```
$ AWS_PROFILE=testprofile awssh
```

리전 지정:

```
$ AWS_REGION=us-west-2 awssh
```

running/pending 인스턴스 이름과 아이디 출력:

```
$ awssh --list
```

리모트 서버에 명령어 실행:

```
$ awssh -c 'pwd' <remote-server-name>
```

## Notes

-  사용자가 모든 SSH 키를 '$HOME/.ssh/'에 보관하고 EC2 인스턴스에 할당된 키 이름과 일치한다고 가정합니다. 개인 키의 대체 경로를 지정하려면 '-p' 또는 'AWS_KEY_DIR'을 사용합니다.
