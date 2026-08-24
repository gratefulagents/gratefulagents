def execute(data):
    encode = lambda fields: "".join(fields)
    return {"authorized": encode(data["signed"]) == encode(data["requested"])}
