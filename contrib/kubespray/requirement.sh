#!/bin/bash

while getopts "w:m:c:" opt; do
  case $opt in
    w)
      WORKER_LIST=$OPTARG
      ;;
    m)
      MASTER_LIST=$OPTARG
      ;;
    c)
      CLUSTER_CONFIG=$OPTARG
      ;;
    \?)
      echo "Invalid option: -$OPTARG"
      exit 1
      ;;
  esac
done

echo "worker list file path: ${WORKER_LIST}"
echo "master list file path: ${MASTER_LIST}"
echo "cluster config file path: ${CLUSTER_CONFIG}"

mkdir -p ${HOME}/pai-pre-check/
python3 script/pre-check-generator.py -m ${MASTER_LIST} -w ${WORKER_LIST} -c ${CLUSTER_CONFIG} -o ${HOME}/pai-pre-check

ABS_CONFIG_PATH="$(cd "$CLUSTER_CONFIG" && pwd -P)"
echo "Config path is: ${ABS_CONFIG_PATH}"
ansible-playbook -i ${HOME}/pai-pre-check/pre-check.yml environment-check.yml -e "@${ABS_CONFIG_PATH}"
ret_code_check=$?

if [ $ret_code_check -eq 0 ]
then
  echo "Pass: Cluster meets the requirements"
else
  echo "Faild: Please check the output, and modify the cluster setting to meet the requirement"
fi

rm -rf ${HOME}/pai-pre-check/

