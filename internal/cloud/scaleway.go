package cloud

import (
	"strings"

	"github.com/pkg/errors"
	"github.com/protosio/cli/internal/ssh"
	account "github.com/scaleway/scaleway-sdk-go/api/account/v2alpha1"
	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	"github.com/scaleway/scaleway-sdk-go/api/marketplace/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	log "github.com/sirupsen/logrus"
)

const (
	scalewayArch = "x86_64"
	uploadSSHkey = "protos-upload-key"
)

type scalewayCredentials struct {
	organisationID string
	accessKey      string
	secretKey      string
}

type scaleway struct {
	credentials    *scalewayCredentials
	client         *scw.Client
	instanceAPI    *instance.API
	accountAPI     *account.API
	marketplaceAPI *marketplace.API
}

func newScalewayClient() (*scaleway, error) {
	scaleway := &scaleway{}

	return scaleway, nil

}

// NewInstance creates a new Protos instance on Scaleway
func (sw *scaleway) NewInstance(name string, imageID string, pubKey string) (string, error) {

	//
	// create SSH key
	//

	keysResp, err := sw.accountAPI.ListSSHKeys(&account.ListSSHKeysRequest{})
	if err != nil {
		return "", errors.Wrap(err, "Failed to get SSH keys")
	}
	for _, k := range keysResp.SSHKeys {
		if k.Name == name {
			log.Infof("Found an SSH key with the same name as the instance (%s). Deleting it and creating a new key for the current instance.", name)
			sw.accountAPI.DeleteSSHKey(&account.DeleteSSHKeyRequest{SSHKeyID: k.ID})
		}
	}

	pubKey = strings.TrimSuffix(pubKey, "\n") + " root@protos.io"
	_, err = sw.accountAPI.CreateSSHKey(&account.CreateSSHKeyRequest{Name: name, OrganizationID: sw.credentials.organisationID, PublicKey: pubKey})
	if err != nil {
		return "", errors.Wrap(err, "Failed to add SSH key for instance")
	}

	//
	// create server

	// checking if there is a server with the same name
	serversResp, err := sw.instanceAPI.ListServers(&instance.ListServersRequest{Zone: scw.ZoneNlAms1})
	if err != nil {
		return "", errors.Wrap(err, "Failed to retrieve servers")
	}
	for _, srv := range serversResp.Servers {
		if srv.Name == name {
			return "", errors.Errorf("There is already an instance with name '%s' on Scaleway, in zone '%s'", name, scw.ZoneNlAms1)
		}
	}

	// deploying the instance
	volumeMap := make(map[string]*instance.VolumeTemplate)
	log.Infof("Deploing VM using image '%s'", imageID)
	ipreq := true
	req := &instance.CreateServerRequest{
		Name:              name,
		Zone:              scw.ZoneNlAms1,
		CommercialType:    "DEV1-S",
		DynamicIPRequired: &ipreq,
		EnableIPv6:        false,
		BootType:          instance.BootTypeLocal,
		Image:             imageID,
		Volumes:           volumeMap,
	}

	srvResp, err := sw.instanceAPI.CreateServer(req)
	if err != nil {
		return "", errors.Wrap(err, "Failed to create VM")
	}
	log.Infof("Created server '%s' (%s)", srvResp.Server.Name, srvResp.Server.ID)

	return srvResp.Server.ID, nil
}

func (sw *scaleway) DeleteInstance(id string) error {
	err := sw.instanceAPI.DeleteServer(&instance.DeleteServerRequest{Zone: scw.ZoneNlAms1, ServerID: id})
	if err != nil {
		return errors.Wrapf(err, "Failed to delete instance '%s'", id)
	}
	return nil
}

func (sw *scaleway) StartInstance(id string) error {
	startReq := &instance.ServerActionAndWaitRequest{
		ServerID: id,
		Zone:     scw.ZoneNlAms1,
		Action:   instance.ServerActionPoweron,
	}
	err := sw.instanceAPI.ServerActionAndWait(startReq)
	if err != nil {
		return errors.Wrap(err, "Failed to start Scaleway instance")
	}
	return nil
}

func (sw *scaleway) StopInstance(id string) error {
	stopReq := &instance.ServerActionAndWaitRequest{
		ServerID: id,
		Zone:     scw.ZoneNlAms1,
		Action:   instance.ServerActionPoweroff,
	}
	err := sw.instanceAPI.ServerActionAndWait(stopReq)
	if err != nil {
		return errors.Wrap(err, "Failed to stop Scaleway instance")
	}
	return nil
}

func (sw *scaleway) GetInstanceInfo(id string) (InstanceInfo, error) {
	resp, err := sw.instanceAPI.GetServer(&instance.GetServerRequest{ServerID: id, Zone: scw.ZoneNlAms1})
	if err != nil {
		return InstanceInfo{}, errors.Wrapf(err, "Failed to retrieve Scaleway instance (%s) information", id)
	}
	info := InstanceInfo{ID: id, Name: resp.Server.Name, Location: string(scw.ZoneNlAms1)}
	if resp.Server.PublicIP != nil {
		info.PublicIP = resp.Server.PublicIP.Address.String()
	}
	return info, nil
}

func (sw *scaleway) GetImages() (map[string]string, error) {
	images := map[string]string{}
	resp, err := sw.instanceAPI.ListImages(&instance.ListImagesRequest{Zone: scw.ZoneNlAms1})
	if err != nil {
		return images, errors.Wrap(err, "Failed to retrieve account images from Scaleway")
	}
	for _, img := range resp.Images {
		images[img.Name] = img.ID
	}
	return images, nil
}

func (sw *scaleway) AddImage(url string, hash string) (string, error) {

	//
	// create and add ssh key to account
	//

	key, err := ssh.GenerateKey()
	if err != nil {
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway")
	}
	pubKey := strings.TrimSuffix(key.Public(), "\n") + " root@protos.io"

	sshKey, err := sw.accountAPI.CreateSSHKey(&account.CreateSSHKeyRequest{Name: uploadSSHkey, OrganizationID: sw.credentials.organisationID, PublicKey: pubKey})
	if err != nil {
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway: Failed to add temporary SSH key")
	}
	defer sw.cleanImageSSHkeys(sshKey.ID)

	//
	// find correct image
	//

	imageID, err := sw.getUploadImageID(scw.ZoneNlAms1)
	if err != nil {
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway")
	}

	log.Infof("Using image '%s' for adding Protos image to Scaleway", imageID)

	//
	// create upload server
	//

	srv, vol, err := sw.createImageUploadVM(imageID)
	if err != nil {
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway")
	}
	defer sw.cleanImageUploadVM(srv)

	//
	// connect via SSH, download Protos image and write it to a volume
	//

	log.Info("Trying to connect to Scaleway upload instance over SSH")

	sshClient, err := ssh.NewConnection(srv.PublicIP.Address.String(), "root", key.SSHAuth(), 10)
	if err != nil {
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway. Failed to deploy VM to Scaleway")
	}
	log.Info("SSH connection initiated")

	//
	// wite Protos image to volume
	//

	log.Info("Downloading Protos image")
	out, err := ssh.ExecuteCommand("wget -P /tmp https://releases.protos.io/test/scaleway-efi.iso", sshClient)
	if err != nil {
		log.Errorf("Error downloading Protos VM image: %s", out)
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway. Error downloading Protos VM image")
	}

	out, err = ssh.ExecuteCommand("ls /dev/vdb", sshClient)
	if err != nil {
		log.Errorf("Snapshot volume not found: %s", out)
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway. Snapshot volume not found")
	}

	log.Info("Writing Protos image to volume")
	out, err = ssh.ExecuteCommand("dd if=/tmp/scaleway-efi.iso of=/dev/vdb", sshClient)
	if err != nil {
		log.Errorf("Error while writing image to volume: %s", out)
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway. Error while writing image to volume")
	}

	//
	// turn off upload VM and dettach volume
	//

	log.Infof("Stopping upload server '%s' (%s)", srv.Name, srv.ID)
	stopReq := &instance.ServerActionAndWaitRequest{
		ServerID: srv.ID,
		Zone:     scw.ZoneNlAms1,
		Action:   instance.ServerActionPoweroff,
	}
	err = sw.instanceAPI.ServerActionAndWait(stopReq)
	if err != nil {
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway. Error while stopping upload server")
	}

	_, err = sw.instanceAPI.DetachVolume(&instance.DetachVolumeRequest{Zone: scw.ZoneNlAms1, VolumeID: vol.ID})
	if err != nil {
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway. Error while detaching image volume")
	}

	//
	// create snapshot and image
	//

	log.Info("Creating snapshot from volume")
	snapshotResp, err := sw.instanceAPI.CreateSnapshot(&instance.CreateSnapshotRequest{
		VolumeID: vol.ID,
		Name:     "protos-image-snapshot",
		Zone:     scw.ZoneNlAms1,
	})
	if err != nil {
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway. Error while creating snapshot from volume")
	}

	log.Info("Creating image from snapshot")
	imageResp, err := sw.instanceAPI.CreateImage(&instance.CreateImageRequest{
		Name:       "protos-image",
		Arch:       instance.ArchX86_64,
		RootVolume: snapshotResp.Snapshot.ID,
		Zone:       scw.ZoneNlAms1,
	})
	if err != nil {
		return "", errors.Wrap(err, "Failed to add Protos image to Scaleway. Error while creating image from snapshot")
	}
	log.Infof("Protos image '%s' created", imageResp.Image.ID)

	log.Infof("Deleting protos image volume '%s'", vol.ID)
	err = sw.instanceAPI.DeleteVolume(&instance.DeleteVolumeRequest{Zone: scw.ZoneNlAms1, VolumeID: vol.ID})
	if err != nil {
		return "", errors.Wrap(err, "Error while removing protos image volume. Manual clean might be needed")
	}

	return imageResp.Image.ID, nil
}

func (sw *scaleway) RemoveImage(id string) error {
	return nil
}

func (sw *scaleway) NewVolume(name string, size int) (string, error) {
	sizeVolume := scw.Size(uint64(size * 1048576))
	createVolumeReq := &instance.CreateVolumeRequest{
		Name:       name,
		VolumeType: "b_ssd",
		Size:       &sizeVolume,
		Zone:       sw.location,
	}

	volumeResp, err := sw.instanceAPI.CreateVolume(createVolumeReq)
	if err != nil {
		return "", errors.Wrap(err, "Failed to create Scaleway volume")
	}
	return volumeResp.Volume.ID, nil
}

func (sw *scaleway) DeleteVolume(id string) error {
	deleteVolumeReq := &instance.DeleteVolumeRequest{
		VolumeID: id,
		Zone:     sw.location,
	}
	err := sw.instanceAPI.DeleteVolume(deleteVolumeReq)
	if err != nil {
		return errors.Wrapf(err, "Failed to delete Scaleway volume '%s'", id)
	}
	return nil
}

func (sw *scaleway) AttachVolume(volumeID string, instanceID string) error {
	return nil
}

func (sw *scaleway) DettachVolume(volumeID string, instanceID string) error {
	return nil
}

func (sw *scaleway) AuthFields() []string {
	return []string{"ORGANISATION_ID", "ACCESS_KEY", "SECRET_KEY"}
}

func (sw *scaleway) Init(auth map[string]string) error {
	var err error

	scwCredentials := &scalewayCredentials{}
	for k, v := range auth {
		switch k {
		case "ORGANISATION_ID":
			scwCredentials.organisationID = v
		case "ACCESS_KEY":
			scwCredentials.accessKey = v
		case "SECRET_KEY":
			scwCredentials.secretKey = v
		default:
			return errors.Errorf("Credentials field '%s' not supported for Scaleway cloud provider", k)
		}
		if v == "" {
			return errors.Errorf("Credentials field '%s' is empty", k)
		}
	}

	sw.credentials = scwCredentials
	sw.client, err = scw.NewClient(
		scw.WithDefaultOrganizationID(scwCredentials.organisationID),
		scw.WithAuth(scwCredentials.accessKey, scwCredentials.secretKey),
	)
	if err != nil {
		return errors.Wrap(err, "Failed to init Scaleway client")
	}

	sw.instanceAPI = instance.NewAPI(sw.client)
	sw.accountAPI = account.NewAPI(sw.client)
	sw.marketplaceAPI = marketplace.NewAPI(sw.client)
	_, err = sw.accountAPI.ListSSHKeys(&account.ListSSHKeysRequest{})
	if err != nil {
		return errors.Wrap(err, "Failed to init Scaleway client")
	}
	return nil
}

//
// helper methods
//

func (sw *scaleway) getUploadImageID(zone scw.Zone) (string, error) {
	resp, err := sw.marketplaceAPI.ListImages(&marketplace.ListImagesRequest{})
	if err != nil {
		return "", errors.Wrap(err, "Failed to retrieve marketplace images from Scaleway")
	}
	for _, img := range resp.Images {
		if img.Name == "Ubuntu Bionic" {
			for _, ver := range img.Versions {
				for _, li := range ver.LocalImages {
					if li.Arch == scalewayArch && li.Zone == zone {
						return li.ID, nil
					}
				}
			}
		}
	}
	return "", errors.Errorf("Ubuntu Bionic image in zone '%s' not found", scw.ZoneFrPar1)
}

func (sw *scaleway) cleanImageSSHkeys(keyID string) {
	err := sw.accountAPI.DeleteSSHKey(&account.DeleteSSHKeyRequest{SSHKeyID: keyID})
	if err != nil {
		log.Error(errors.Wrapf(err, "Failed to clean up Scaleway image upload key with id '%s'", keyID))
	}
	log.Infof("Deleted SSH key '%s'", keyID)
}

func (sw *scaleway) createImageUploadVM(imageID string) (*instance.Server, *instance.Volume, error) {

	//
	// create volume
	//

	size := scw.Size(uint64(10000000000))
	createVolumeReq := &instance.CreateVolumeRequest{
		Name:       "protos-image-uploader",
		VolumeType: "l_ssd",
		Size:       &size,
		Zone:       scw.ZoneNlAms1,
	}

	log.Info("Creating image volume")
	volumeResp, err := sw.instanceAPI.CreateVolume(createVolumeReq)
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to create image volume")
	}

	//
	// create server
	//

	volumeMap := make(map[string]*instance.VolumeTemplate)
	volumeTemplate := &instance.VolumeTemplate{
		Size: size,
	}
	volumeMap["0"] = volumeTemplate

	ipreq := true
	req := &instance.CreateServerRequest{
		Name:              "protos-image-uploader",
		Zone:              scw.ZoneNlAms1,
		CommercialType:    "DEV1-S",
		DynamicIPRequired: &ipreq,
		EnableIPv6:        false,
		BootType:          instance.BootTypeLocal,
		Image:             imageID,
		Volumes:           volumeMap,
	}

	srvResp, err := sw.instanceAPI.CreateServer(req)
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to create upload VM")
	}
	log.Infof("Created server '%s' (%s)", srvResp.Server.Name, srvResp.Server.ID)

	//
	// attach volume
	//

	log.Info("Attaching snapshot volume to upload VM")
	attachVolumeReq := &instance.AttachVolumeRequest{
		ServerID: srvResp.Server.ID,
		VolumeID: volumeResp.Volume.ID,
		Zone:     scw.ZoneNlAms1,
	}

	_, err = sw.instanceAPI.AttachVolume(attachVolumeReq)
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to attach volume to upload VM")
	}

	//
	// start server
	//

	// default timeout is 5 minutes
	log.Infof("Starting and waiting for server '%s' (%s)", srvResp.Server.Name, srvResp.Server.ID)
	startReq := &instance.ServerActionAndWaitRequest{
		ServerID: srvResp.Server.ID,
		Zone:     scw.ZoneNlAms1,
		Action:   instance.ServerActionPoweron,
	}
	err = sw.instanceAPI.ServerActionAndWait(startReq)
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to start upload server")
	}
	log.Infof("Server '%s' (%s) started successfully", srvResp.Server.Name, srvResp.Server.ID)

	//
	// refresh IP info
	//

	srvStatusResp, err := sw.instanceAPI.GetServer(&instance.GetServerRequest{ServerID: srvResp.Server.ID, Zone: scw.ZoneNlAms1})
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to retrieve upload VM details")
	}

	return srvStatusResp.Server, volumeResp.Volume, nil
}

func (sw *scaleway) cleanImageUploadVM(srv *instance.Server) {
	srvStatusResp, err := sw.instanceAPI.GetServer(&instance.GetServerRequest{ServerID: srv.ID, Zone: scw.ZoneNlAms1})
	if err != nil {
		log.Error(errors.Wrap(err, "Failed to refresh upload server info"))
		return
	}
	srv = srvStatusResp.Server

	if srv.State == instance.ServerStateRunning {
		// default timeout is 5 minutes
		log.Infof("Stopping and waiting for server '%s' (%s)", srv.Name, srv.ID)
		stopReq := &instance.ServerActionAndWaitRequest{
			ServerID: srv.ID,
			Zone:     scw.ZoneNlAms1,
			Action:   instance.ServerActionPoweroff,
		}
		err = sw.instanceAPI.ServerActionAndWait(stopReq)
		if err != nil {
			log.Error(errors.Wrap(err, "Failed to stop upload server"))
			return
		}
		log.Infof("Server '%s' (%s) stopped successfully", srv.Name, srv.ID)
	}

	for _, vol := range srv.Volumes {
		log.Infof("Deleting volume '%s' for server '%s' (%s)", vol.ID, srv.Name, srv.ID)
		_, err = sw.instanceAPI.DetachVolume(&instance.DetachVolumeRequest{Zone: scw.ZoneNlAms1, VolumeID: vol.ID})
		if err != nil {
			log.Errorf("Failed to dettach volume '%s' for server '%s' (%s): %s", vol.ID, srv.Name, srv.ID, err.Error())
			continue
		}
		err = sw.instanceAPI.DeleteVolume(&instance.DeleteVolumeRequest{Zone: scw.ZoneNlAms1, VolumeID: vol.ID})
		if err != nil {
			log.Errorf("Failed to delete volume '%s' for server '%s' (%s): %s", vol.ID, srv.Name, srv.ID, err.Error())
		}
	}

	log.Infof("Deleting server '%s' (%s)", srv.Name, srv.ID)
	err = sw.instanceAPI.DeleteServer(&instance.DeleteServerRequest{ServerID: srv.ID, Zone: scw.ZoneNlAms1})
	if err != nil {
		log.Error(errors.Wrap(err, "Failed to add Protos image to Scaleway"))
		return
	}
}
